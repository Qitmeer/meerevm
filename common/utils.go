/*
 * Copyright (c) 2017-2020 The qitmeer developers
 */

package common

import (
	"encoding/hex"
	"github.com/Qitmeer/qng-core/common/hash"
	"github.com/Qitmeer/qng-core/core/blockchain/opreturn"
	qtypes "github.com/Qitmeer/qng-core/core/types"
	"github.com/Qitmeer/qng-core/crypto/ecc"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/params"
	"math/big"
	"time"
)

func ReverseBytes(bs *[]byte) {
	length := len(*bs)
	for i := 0; i < length/2; i++ {
		index := length - 1 - i
		temp := (*bs)[index]
		(*bs)[index] = (*bs)[i]
		(*bs)[i] = temp
	}
}

func NewMeerEVMAddress(pubkeyHex string) (common.Address, error) {
	pubkBytes, err := hex.DecodeString(pubkeyHex)
	if err != nil {
		return common.Address{}, err
	}

	publicKey, err := ecc.Secp256k1.ParsePubKey(pubkBytes)
	if err != nil {
		return common.Address{}, err
	}
	return crypto.PubkeyToAddress(*publicKey.ToECDSA()), nil
}

var (
	Precision = big.NewInt(params.Ether).Div(big.NewInt(params.Ether), big.NewInt(qtypes.AtomsPerCoin))
)

func CopyReceipts(receipts []*types.Receipt) []*types.Receipt {
	result := make([]*types.Receipt, len(receipts))
	for i, l := range receipts {
		cpy := *l
		result[i] = &cpy
	}
	return result
}

func TotalFees(block *types.Block, receipts []*types.Receipt) *big.Float {
	feesWei := new(big.Int)
	for i, tx := range block.Transactions() {
		minerFee, _ := tx.EffectiveGasTip(block.BaseFee())
		feesWei.Add(feesWei, new(big.Int).Mul(new(big.Int).SetUint64(receipts[i].GasUsed), minerFee))
	}
	return new(big.Float).Quo(new(big.Float).SetInt(feesWei), new(big.Float).SetInt(big.NewInt(params.Ether)))
}

func ToEVMHash(h *hash.Hash) common.Hash {
	ehb := h.Bytes()
	ReverseBytes(&ehb)
	return common.BytesToHash(ehb)
}

func ToQNGTx(tx *types.Transaction, timestamp int64) *qtypes.Transaction {
	txmb, err := tx.MarshalBinary()
	if err != nil {
		return nil
	}
	txmbHex := hexutil.Encode(txmb)

	qtxhb := tx.Hash().Bytes()
	ReverseBytes(&qtxhb)
	qtxh := hash.MustBytesToHash(qtxhb)

	mtx := qtypes.NewTransaction()

	if timestamp > 0 {
		mtx.Timestamp = time.Unix(timestamp, 0)
	}

	mtx.AddTxIn(&qtypes.TxInput{
		PreviousOut: *qtypes.NewOutPoint(&qtxh, qtypes.SupperPrevOutIndex),
		Sequence:    uint32(qtypes.TxTypeCrossChainVM),
		AmountIn:    qtypes.Amount{Id: qtypes.ETHID, Value: 0},
		SignScript:  []byte(txmbHex),
	})
	mtx.AddTxOut(&qtypes.TxOutput{
		Amount:   qtypes.Amount{Value: 0, Id: qtypes.ETHID},
		PkScript: opreturn.NewEVMTx().PKScript(),
	})

	return mtx
}
