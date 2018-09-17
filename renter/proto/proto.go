// Package proto implements the renter side of the Sia renter-host protocol.
package proto // import "lukechampine.com/us/renter/proto"

import (
	"time"

	"gitlab.com/NebulousLabs/Sia/crypto"
	"gitlab.com/NebulousLabs/Sia/modules"
	"gitlab.com/NebulousLabs/Sia/types"
	"lukechampine.com/us/hostdb"

	"github.com/pkg/errors"
)

// ErrDesynchronized is returned by ContractEditor.SyncWithHost to indicate
// that synchronization is impossible.
var ErrDesynchronized = errors.New("renter contract has permanently desynchronized from host")

type (
	// A Wallet provides addresses and outputs, and can sign transactions.
	Wallet interface {
		NewWalletAddress() (types.UnlockHash, error)
		SignTransaction(txn *types.Transaction, toSign []crypto.Hash) error
		UnspentOutputs() []modules.UnspentOutput
		UnlockConditions(addr types.UnlockHash) (types.UnlockConditions, error)
	}
	// A TransactionPool can broadcast transactions and estimate transaction
	// fees.
	TransactionPool interface {
		AcceptTransactionSet([]types.Transaction) error
		FeeEstimate() (min types.Currency, max types.Currency)
	}
)

// A ContractEditor provides an interface for viewing and updating a file
// contract transaction and the Merkle roots of each sector covered by the
// contract.
type ContractEditor interface {
	// Revision returns the latest revision of the file contract.
	Revision() ContractRevision

	// AppendRoot appends a sector root to the contract, returning the new
	// top-level Merkle root. The root should be written to durable storage.
	AppendRoot(root crypto.Hash) (crypto.Hash, error)

	// NumSectors returns the number of sector roots in the contract.
	NumSectors() int

	// SyncWithHost synchronizes the local version of the contract with the
	// host's version. This may involve modifying the sector roots and/or
	// contract revision. SyncWithHost returns ErrDesynchronized iff the
	// contract has permanently desynchronized with the host and recovery is
	// impossible.
	SyncWithHost(rev types.FileContractRevision, hostSignatures []types.TransactionSignature) error
}

// A ContractRevision contains a file contract transaction and the secret
// key used to sign it.
type ContractRevision struct {
	Revision   types.FileContractRevision
	Signatures [2]types.TransactionSignature
	RenterKey  crypto.SecretKey
}

// EndHeight returns the height at which the host is no longer obligated to
// store contract data.
func (c ContractRevision) EndHeight() types.BlockHeight {
	return c.Revision.NewWindowStart
}

// ID returns the ID of the original FileContract.
func (c ContractRevision) ID() types.FileContractID {
	return c.Revision.ParentID
}

// HostKey returns the public key of the host.
func (c ContractRevision) HostKey() hostdb.HostPublicKey {
	key := c.Revision.UnlockConditions.PublicKeys[1]
	return hostdb.HostPublicKey(key.String())
}

// RenterFunds returns the funds remaining in the contract's Renter payout as
// of the most recent revision.
func (c ContractRevision) RenterFunds() types.Currency {
	return c.Revision.NewValidProofOutputs[0].Value
}

// IsValid returns false if the ContractRevision has the wrong number of
// public keys or outputs.
func (c ContractRevision) IsValid() bool {
	return len(c.Revision.NewValidProofOutputs) > 0 &&
		len(c.Revision.UnlockConditions.PublicKeys) == 2
}

// SubmitContractRevision submits the latest revision of a contract to the
// blockchain, finalizing the renter and host payouts as they stand in the
// revision. Submitting a revision with a higher revision number will replace
// the previously-submitted revision.
//
// Submitting revision transactions is a way for the renter to punish the
// host. If the host is well-behaved, there is no incentive for the renter to
// submit revision transactions. But if the host misbehaves, submitting the
// revision ensures that the host will lose the collateral it committed.
func SubmitContractRevision(c ContractRevision, w Wallet, tpool TransactionPool) error {
	// construct a transaction containing the signed revision
	txn := types.Transaction{
		FileContractRevisions: []types.FileContractRevision{c.Revision},
		TransactionSignatures: c.Signatures[:],
	}

	// add the transaction fee
	_, maxFee := tpool.FeeEstimate()
	fee := maxFee.Mul64(estTxnSize)
	txn.MinerFees = append(txn.MinerFees, fee)

	// pay for the fee by adding outputs and signing them
	changeAddr, err := w.NewWalletAddress()
	if err != nil {
		return errors.Wrap(err, "could not get a change address to use")
	}
	toSign, ok := fundSiacoins(&txn, fee, changeAddr, w)
	if !ok {
		return errors.New("not enough coins to fund transaction fee")
	}
	if err := w.SignTransaction(&txn, toSign); err != nil {
		return errors.Wrap(err, "failed to sign transaction")
	}

	// submit the funded and signed transaction
	if err := tpool.AcceptTransactionSet([]types.Transaction{txn}); err != nil {
		return err
	}
	return nil
}

// DialStats records metrics about dialing a host.
type DialStats struct {
	DialStart     time.Time `json:"dialStart"`
	ProtocolStart time.Time `json:"protocolStart"`
	ProtocolEnd   time.Time `json:"protocolEnd"`
}

// DownloadStats records metrics about downloading sector data from a host.
type DownloadStats struct {
	Bytes         int64          `json:"bytes"`
	Cost          types.Currency `json:"cost"`
	ProtocolStart time.Time      `json:"protocolStart"`
	ProtocolEnd   time.Time      `json:"protocolEnd"`
	TransferStart time.Time      `json:"transferStart"`
	TransferEnd   time.Time      `json:"transferEnd"`
}

// UploadStats records metrics about uploading sector data to a host.
type UploadStats struct {
	Bytes         int64          `json:"bytes"`
	Cost          types.Currency `json:"cost"`
	Collateral    types.Currency `json:"collateral"`
	ProtocolStart time.Time      `json:"protocolStart"`
	ProtocolEnd   time.Time      `json:"protocolEnd"`
	TransferStart time.Time      `json:"transferStart"`
	TransferEnd   time.Time      `json:"transferEnd"`
}
