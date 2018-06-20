package db

import (
	"github.com/cosmos/cosmos-academy/example-apps/token_curated_registry/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/bank"
	"github.com/tendermint/go-amino"
)

type BallotKeeper struct {
	ListingKey sdk.StoreKey

	BallotKey sdk.StoreKey

	Cdc *amino.Codec
}

func NewBallotKeeper(listingKey sdk.StoreKey, ballotkey sdk.StoreKey, _cdc *amino.Codec) BallotKeeper {
	return BallotKeeper{
		ListingKey: listingKey,
		BallotKey:  ballotkey,
		Cdc:        _cdc,
	}
}

// Will get Ballot using unique identifier. Do not need to specify status
func (bk BallotKeeper) GetBallot(ctx sdk.Context, identifier string) types.Ballot {
	store := ctx.KVStore(bk.BallotKey)
	key := []byte(identifier)
	val := store.Get(key)
	if val == nil {
		return types.Ballot{}
	}
	ballot := &types.Ballot{}
	err := bk.Cdc.UnmarshalBinary(val, ballot)
	if err != nil {
		panic(err)
	}
	return *ballot
}

func (bk BallotKeeper) AddBallot(ctx sdk.Context, identifier string, owner sdk.Address, applyLen int64, bond int64) sdk.Error {
	store := ctx.KVStore(bk.BallotKey)

	newBallot := types.Ballot{
		Identifier:         identifier,
		Owner:              owner,
		Bond:               bond,
		EndApplyBlockStamp: ctx.BlockHeight() + applyLen,
	}
	// Add ballot with Pending Status
	key := []byte(identifier)
	val, _ := bk.Cdc.MarshalBinary(newBallot)
	store.Set(key, val)
	return nil
}

func (bk BallotKeeper) ActivateBallot(ctx sdk.Context, accountKeeper bank.Keeper, owner sdk.Address, challenger sdk.Address, 
	identifier string, commitLen int64, revealLen, minBond int64, challengeBond int64) sdk.Error {
	store := ctx.KVStore(bk.BallotKey)
	ballot := bk.GetBallot(ctx, identifier)

	if ballot.Bond < minBond {
		bk.DeleteBallot(ctx, identifier)
		refund := sdk.Coin{
			Denom:  "RegistryCoin",
			Amount: challengeBond,
		}
		_, _, err := accountKeeper.AddCoins(ctx, challenger, []sdk.Coin{refund})
		if err != nil {
			return err
		}
		return nil
	}
	if ballot.Bond != challengeBond {
		return sdk.NewError(2, 115, "Must match candidate's bond")
	}

	ballot.Active = true
	ballot.Challenger = challenger
	ballot.EndCommitBlockStamp = ctx.BlockHeight() + commitLen
	ballot.EndApplyBlockStamp = ballot.EndCommitBlockStamp + revealLen

	newBallot, _ := bk.Cdc.MarshalBinary(ballot)
	key := []byte(identifier)
	store.Set(key, newBallot)

	return nil
}

func (bk BallotKeeper) VoteBallot(ctx sdk.Context, owner sdk.Address, identifier string, vote bool, power int64) sdk.Error {
	ballotStore := ctx.KVStore(bk.BallotKey)

	ballotKey := []byte(identifier)
	bz := ballotStore.Get(ballotKey)
	if bz == nil {
		return sdk.NewError(2, 107, "Ballot does not exist")
	}
	ballot := &types.Ballot{}
	err := bk.Cdc.UnmarshalBinary(bz, ballot)
	if err != nil {
		panic(err)
	}
	if vote {
		ballot.Approve += power
	} else {
		ballot.Deny += power
	}
	newBallot, _ := bk.Cdc.MarshalBinary(*ballot)
	ballotStore.Set(ballotKey, newBallot)

	return nil
}

func (bk BallotKeeper) DeleteBallot(ctx sdk.Context, identifier string) {
	key := []byte(identifier)
	store := ctx.KVStore(bk.BallotKey)
	store.Delete(key)
}

func (bk BallotKeeper) AddListing(ctx sdk.Context, identifier string, votes int64) {
	key := []byte(identifier)
	store := ctx.KVStore(bk.ListingKey)

	listing := types.Listing{
		Identifier: identifier,
		Votes:      votes,
	}
	val, _ := bk.Cdc.MarshalBinary(listing)

	store.Set(key, val)
}

func (bk BallotKeeper) GetListing(ctx sdk.Context, identifier string) types.Listing {
	key := []byte(identifier)
	store := ctx.KVStore(bk.ListingKey)

	bz := store.Get(key)
	if bz == nil {
		return types.Listing{}
	}
	listing := &types.Listing{}
	err := bk.Cdc.UnmarshalBinary(bz, listing)
	if err != nil {
		panic(err)
	}

	return *listing
}

func (bk BallotKeeper) DeleteListing(ctx sdk.Context, identifier string) {
	key := []byte(identifier)
	store := ctx.KVStore(bk.ListingKey)

	store.Delete(key)
}
