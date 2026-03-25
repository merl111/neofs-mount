package neofs

import (
	"context"
	"fmt"
	"math"
	"math/big"

	"github.com/nspcc-dev/neo-go/pkg/encoding/address"
	"github.com/nspcc-dev/neo-go/pkg/rpcclient"
	"github.com/nspcc-dev/neo-go/pkg/rpcclient/actor"
	"github.com/nspcc-dev/neo-go/pkg/rpcclient/gas"
	"github.com/nspcc-dev/neo-go/pkg/wallet"
)

const (
	mainnetContract = "NNxVrKjLsRkWsmGgmuNXLcMswtxTGaNQLk"
	testnetContract = "NZAUkYbJ1Cb2HrNmwZ1pg9xYHBhm2FgtKV"
	mainnetRPC      = "https://mainnet1.neo.coz.io:443"
	testnetRPC      = "https://testnet1.neo.coz.io:443"
)

// TopUpGAS transfers GAS from the wallet to the neoFS smart contract on the specified network.
func TopUpGAS(ctx context.Context, wif, network, rpcOverride string, amount float64) (string, error) {
	w, err := wallet.NewAccountFromWIF(wif)
	if err != nil {
		return "", fmt.Errorf("invalid wallet credentials: %w", err)
	}

	contractStr := mainnetContract
	rpcURL := mainnetRPC

	if network == "testnet" {
		contractStr = testnetContract
		rpcURL = testnetRPC
	}

	if rpcOverride != "" {
		rpcURL = rpcOverride
	}

	contractHash, err := address.StringToUint160(contractStr)
	if err != nil {
		return "", fmt.Errorf("invalid contract address: %w", err)
	}

	c, err := rpcclient.New(ctx, rpcURL, rpcclient.Options{})
	if err != nil {
		return "", fmt.Errorf("RPC connection failed: %w", err)
	}
	defer c.Close()

	if err := c.Init(); err != nil {
		return "", fmt.Errorf("RPC connection init failed: %w", err)
	}

	act, err := actor.NewSimple(c, w)
	if err != nil {
		return "", fmt.Errorf("failed to create actor: %w", err)
	}

	g := gas.New(act)

	// GAS has 8 decimals precision natively on Neo N3
	amountInt := int64(amount * math.Pow10(8))

	txHash, vub, err := g.Transfer(w.Contract.ScriptHash(), contractHash, big.NewInt(amountInt), nil)
	if err != nil {
		return "", fmt.Errorf("transfer failed: %w", err)
	}

	// wait for transaction success on-chain
	res, err := act.WaitSuccess(ctx, txHash, vub, err)
	if err != nil {
		return txHash.StringLE(), fmt.Errorf("transaction failed or not accepted (tx: %s): %w", txHash.StringLE(), err)
	}

	if res.VMState.String() == "FAULT" {
		return txHash.StringLE(), fmt.Errorf("transaction FAULT (tx: %s)", txHash.StringLE())
	}

	return txHash.StringLE(), nil
}
