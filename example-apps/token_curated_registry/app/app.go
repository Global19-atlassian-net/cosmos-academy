package app

import (
	"encoding/json"
	handle "github.com/cosmos/cosmos-academy/example-apps/token_curated_registry/auth"
	dbl "github.com/cosmos/cosmos-academy/example-apps/token_curated_registry/db"
	tcr "github.com/cosmos/cosmos-academy/example-apps/token_curated_registry/types"
	bam "github.com/cosmos/cosmos-sdk/baseapp"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/wire"
	"github.com/cosmos/cosmos-sdk/x/auth"
	"github.com/cosmos/cosmos-sdk/x/bank"
	abci "github.com/tendermint/abci/types"
	"github.com/tendermint/go-amino"
	"github.com/tendermint/go-crypto"
	cmn "github.com/tendermint/tmlibs/common"
	dbm "github.com/tendermint/tmlibs/db"
	"github.com/tendermint/tmlibs/log"
)

const (
	appName = "Registry"
)

// Extended ABCI application
type RegistryApp struct {
	*bam.BaseApp

	cdc *amino.Codec

	minDeposit int64

	applyStage int64

	commitStage int64

	revealStage int64

	dispensationPct float64

	quorum float64

	// keys to access the substores
	capKeyMain     *sdk.KVStoreKey
	capKeyAccount  *sdk.KVStoreKey
	capKeyListings *sdk.KVStoreKey
	capKeyBallots  *sdk.KVStoreKey
	capKeyFees     *sdk.KVStoreKey

	feeKeeper    auth.FeeCollectionKeeper

	ballotKeeper dbl.BallotKeeper

	// Manage addition and subtraction of account balances
	accountMapper auth.AccountMapper
	accountKeeper bank.Keeper
}

// Initializes New Registry App
// CONTRACT: applystage >= commitstage + revealstage
func NewRegistryApp(logger log.Logger, db dbm.DB, mindeposit int64, applystage int64, commitstage int64, revealstage int64, dispensationpct float64, _quorum float64) *RegistryApp {
	cdc := MakeCodec()
	var app = &RegistryApp{
		BaseApp:         bam.NewBaseApp(appName, cdc, logger, db),
		cdc:             cdc,
		minDeposit:      mindeposit,
		applyStage:      applystage,
		commitStage:     commitstage,
		revealStage:     revealstage,
		dispensationPct: dispensationpct,
		quorum:          _quorum,
		capKeyMain:      sdk.NewKVStoreKey("main"),
		capKeyAccount:   sdk.NewKVStoreKey("acc"),
		capKeyFees:      sdk.NewKVStoreKey("fee"),
		capKeyListings:  sdk.NewKVStoreKey("listings"),
		capKeyBallots:   sdk.NewKVStoreKey("ballots"),
	}

	app.feeKeeper = auth.NewFeeCollectionKeeper(cdc, app.capKeyFees)

	app.ballotKeeper = dbl.NewBallotKeeper(app.capKeyListings, app.capKeyBallots, app.cdc)
	app.accountMapper = auth.NewAccountMapper(app.cdc, app.capKeyAccount, &auth.BaseAccount{})
	app.accountKeeper = bank.NewKeeper(app.accountMapper)

	app.Router().
		AddRoute("DeclareCandidacy", handle.NewCandidacyHandler(app.accountKeeper, app.ballotKeeper, app.minDeposit, app.applyStage)).
		AddRoute("Challenge", handle.NewChallengeHandler(app.accountKeeper, app.ballotKeeper, app.commitStage, app.revealStage, app.minDeposit)).
		AddRoute("Commit", handle.NewCommitHandler(app.cdc, app.ballotKeeper)).
		AddRoute("Reveal", handle.NewRevealHandler(app.accountKeeper, app.ballotKeeper))
		
	app.SetTxDecoder(app.txDecoder)
	app.SetInitChainer(app.initChainer)
	app.SetEndBlocker(app.endBlocker)
	app.MountStoresIAVL(app.capKeyMain, app.capKeyAccount, app.capKeyFees, app.capKeyListings, app.capKeyBallots)
	app.SetAnteHandler(auth.NewAnteHandler(app.accountMapper, app.feeKeeper))

	err := app.LoadLatestVersion(app.capKeyMain)
	if err != nil {
		cmn.Exit(err.Error())
	}

	return app
}

func (app *RegistryApp) initChainer(ctx sdk.Context, req abci.RequestInitChain) abci.ResponseInitChain {
	stateJSON := req.AppStateBytes

	genesisState := new(tcr.GenesisState)
	err := app.cdc.UnmarshalJSON(stateJSON, genesisState)
	if err != nil {
		panic(err) // TODO https://github.com/cosmos/cosmos-sdk/issues/468
		// return sdk.ErrGenesisParse("").TraceCause(err, "")
	}

	for _, gacc := range genesisState.Accounts {
		acc, err := gacc.ToAccount()
		if err != nil {
			panic(err) // TODO https://github.com/cosmos/cosmos-sdk/issues/468
			//	return sdk.ErrGenesisParse("").TraceCause(err, "")
		}
		app.accountMapper.SetAccount(ctx, acc)
	}
	return abci.ResponseInitChain{}
}

// EndBlocker finalizes the ballot at the head of the queue if it has passed reveal phase. It also distributes rewards.
func (app *RegistryApp) endBlocker(ctx sdk.Context, req abci.RequestEndBlock) (res abci.ResponseEndBlock) {
	ballot := app.ballotKeeper.ProposalQueueHead(ctx)

	if ctx.BlockHeight() < ballot.EndApplyBlockStamp || ballot.Identifier == "" {
		return
	}

	app.ballotKeeper.ProposalQueuePop(ctx)

	if !ballot.Active {
		// Perhaps put in something other than 0 here
		app.ballotKeeper.AddListing(ctx, ballot.Identifier, 0)
		return 
	}

	total := ballot.Approve + ballot.Deny
	var correctVote bool
	var pool float64
	if float64(ballot.Approve) / float64(total) > app.quorum {
		app.ballotKeeper.AddListing(ctx, ballot.Identifier, ballot.Approve)
		// award proposer dispensationPct of challenger bond
		reward := int64(float64(ballot.Bond) * app.dispensationPct)
		app.accountKeeper.AddCoins(ctx, ballot.Owner, sdk.Coins{{"RegistryCoin", reward}})
		correctVote = true
		pool = float64(ballot.Approve)
	} else {
		app.ballotKeeper.DeleteListing(ctx, ballot.Identifier)
		app.ballotKeeper.DeleteBallot(ctx, ballot.Identifier)

		// award challenger his original bond + dispensationPct of challenger bond
		reward := ballot.Bond + int64(float64(ballot.Bond) * app.dispensationPct)
		app.accountKeeper.AddCoins(ctx, ballot.Challenger, sdk.Coins{{"RegistryCoin", reward}})

		correctVote = false
		pool = float64(ballot.Deny)
	}

	prefixKey := []byte(ballot.Identifier + "votes")
	store := ctx.KVStore(app.capKeyBallots)
	iter := sdk.KVStorePrefixIterator(store, prefixKey)
	
	// May want to limit endBlocker processing to max number of iterations
	var keys [][]byte
	for iter.Valid() {
		keys = append(keys, iter.Key())
		index := len([]byte(ballot.Identifier + "votes"))
		owner := iter.Key()[index:]

		vz := iter.Value()
		vote := &tcr.Vote{}
		app.cdc.UnmarshalBinary(vz, vote)

		if correctVote == vote.Choice {
			reward := vote.Power + int64(float64(vote.Power) / pool * float64(ballot.Bond) * app.dispensationPct)
			app.accountKeeper.AddCoins(ctx, owner, sdk.Coins{{"RegistryCoin", reward}})
		} else {
			app.accountKeeper.AddCoins(ctx, owner, sdk.Coins{{"RegistryCoin", vote.Power}})
		}
		iter.Next()
	}
	iter.Close()

	for _, k := range keys {
		// Delete votes
		store.Delete(k)
	}

	app.ballotKeeper.DeactivateBallot(ctx, ballot.Identifier)

	return abci.ResponseEndBlock{}
}

func (app *RegistryApp) txDecoder(txBytes []byte) (sdk.Tx, sdk.Error) {
	var tx = auth.StdTx{}
	err := app.cdc.UnmarshalBinary(txBytes, &tx)
	if err != nil {
		return nil, sdk.ErrTxDecode("")
	}
	return tx, nil
}

func MakeCodec() *amino.Codec {
	cdc := amino.NewCodec()
	cdc.RegisterInterface((*sdk.Msg)(nil), nil)
	tcr.RegisterAmino(cdc)
	crypto.RegisterAmino(cdc)
	cdc.RegisterInterface((*auth.Account)(nil), nil)
	cdc.RegisterConcrete(&auth.BaseAccount{}, "cosmos-sdk/BaseAccount", nil)
	return cdc
}

// Custom logic for state export
func (app *RegistryApp) ExportAppStateJSON() (appState json.RawMessage, err error) {
	ctx := app.NewContext(true, abci.Header{})

	// iterate to get the accounts
	accounts := []*tcr.GenesisAccount{}
	appendAccount := func(acc auth.Account) (stop bool) {
		account := &tcr.GenesisAccount{
			Address: acc.GetAddress(),
			Coins:   acc.GetCoins(),
		}
		accounts = append(accounts, account)
		return false
	}
	app.accountMapper.IterateAccounts(ctx, appendAccount)

	genState := tcr.GenesisState{
		Accounts: accounts,
	}
	return wire.MarshalJSONIndent(app.cdc, genState)
}
