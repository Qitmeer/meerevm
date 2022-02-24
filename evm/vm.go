/*
 * Copyright (c) 2017-2020 The qitmeer developers
 */

package evm

import (
	"encoding/json"
	"fmt"
	"github.com/Qitmeer/meerevm/chain"
	qcommon "github.com/Qitmeer/meerevm/common"
	"github.com/Qitmeer/meerevm/evm/util"
	"github.com/Qitmeer/qng-core/common/hash"
	"github.com/Qitmeer/qng-core/consensus"
	"github.com/Qitmeer/qng-core/core/address"
	"github.com/Qitmeer/qng-core/core/blockchain/opreturn"
	qtypes "github.com/Qitmeer/qng-core/core/types"
	"github.com/Qitmeer/qng-core/rpc/api"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/event"
	"runtime"
	"sync"
	"time"
)

// meerevm ID of the platform
const (
	MeerEVMID = "meerevm"

	txSlotSize = 32 * 1024
	txMaxSize  = 4 * txSlotSize
)

type VM struct {
	ctx          consensus.Context
	shutdownChan chan struct{}
	shutdownWg   sync.WaitGroup

	chain  *chain.ETHChain
	mchain *chain.MeerChain

	txsCh  chan core.NewTxsEvent
	txsSub event.Subscription
}

func (vm *VM) GetID() string {
	return MeerEVMID
}

func (vm *VM) Initialize(ctx consensus.Context) error {
	util.InitLog(ctx.GetConfig().DebugLevel, ctx.GetConfig().DebugPrintOrigins)

	log.Info("System info", "ETH VM Version", util.Version, "Go version", runtime.Version())

	log.Debug(fmt.Sprintf("Initialize:%s", ctx.GetConfig().DataDir))

	vm.ctx = ctx

	//
	chain.InitEnv(ctx.GetConfig().EVMEnv)

	ethchain, err := chain.NewETHChain(vm.ctx.GetConfig().DataDir)
	if err != nil {
		return err
	}
	vm.chain = ethchain
	vm.mchain = chain.NewMeerChain(ethchain)

	vm.txsSub = ethchain.Ether().TxPool().SubscribeNewTxsEvent(vm.txsCh)

	vm.shutdownWg.Add(1)
	go vm.handler()

	return nil
}

func (vm *VM) Bootstrapping() error {
	log.Debug("Bootstrapping")
	err := vm.chain.Start()
	if err != nil {
		return err
	}
	//
	rpcClient, err := vm.chain.Node().Attach()
	if err != nil {
		log.Error(fmt.Sprintf("Failed to attach to self: %v", err))
	}
	client := ethclient.NewClient(rpcClient)

	blockNum, err := client.BlockNumber(vm.ctx)
	if err != nil {
		log.Error(err.Error())
	} else {
		log.Debug(fmt.Sprintf("MeerETH block chain current block number:%d", blockNum))
	}

	cbh := vm.chain.Ether().BlockChain().CurrentBlock().Header()
	if cbh != nil {
		log.Debug(fmt.Sprintf("MeerETH block chain current block:number=%d hash=%s", cbh.Number.Uint64(), cbh.Hash().String()))
	}

	//
	state, err := vm.chain.Ether().BlockChain().State()
	if err != nil {
		return nil
	}

	log.Debug(fmt.Sprintf("Etherbase:%v balance:%v", vm.chain.Config().Eth.Miner.Etherbase, state.GetBalance(vm.chain.Config().Eth.Miner.Etherbase)))

	//
	for addr := range vm.chain.Config().Eth.Genesis.Alloc {
		log.Debug(fmt.Sprintf("Alloc address:%v balance:%v", addr.String(), state.GetBalance(addr)))
	}
	//
	vm.initTxPool()
	//vm.chain.Ether().Miner().Close()
	return nil
}

func (vm *VM) Bootstrapped() error {
	log.Debug("Bootstrapped")
	return nil
}

func (vm *VM) Shutdown() error {
	log.Debug("Shutdown")
	if vm.ctx == nil {
		return nil
	}

	close(vm.shutdownChan)
	vm.chain.Stop()

	vm.chain.Wait()
	vm.shutdownWg.Wait()
	return nil
}

func (vm *VM) Version() string {
	result := map[string]string{}
	result["MeerVer"] = util.Version
	result["EvmVer"] = vm.chain.Config().Node.Version
	result["ChainID"] = vm.chain.Ether().BlockChain().Config().ChainID.String()
	result["NetworkId"] = fmt.Sprintf("%d", vm.chain.Config().Eth.NetworkId)
	if len(vm.chain.Config().Node.HTTPHost) > 0 {
		result["http"] = fmt.Sprintf("http://%s:%d", vm.chain.Config().Node.HTTPHost, vm.chain.Config().Node.HTTPPort)
	}
	if len(vm.chain.Config().Node.WSHost) > 0 {
		result["ws"] = fmt.Sprintf("ws://%s:%d", vm.chain.Config().Node.WSHost, vm.chain.Config().Node.WSPort)
	}

	resultJson, err := json.Marshal(result)
	if err != nil {
		log.Error(err.Error())
		return ""
	}
	return string(resultJson)
}

func (vm *VM) GetBlock(bh *hash.Hash) (consensus.Block, error) {
	block := vm.chain.Ether().BlockChain().CurrentBlock()
	h := hash.MustBytesToHash(block.Hash().Bytes())
	return &Block{id: &h, ethBlock: block, vm: vm, status: consensus.Accepted}, nil
}

func (vm *VM) BuildBlock(txs []consensus.Tx) (consensus.Block, error) {
	return nil, nil
}

func (vm *VM) CheckConnectBlock(block consensus.Block) error {
	return vm.mchain.CheckConnectBlock(block)
}

func (vm *VM) ConnectBlock(block consensus.Block) error {
	return vm.mchain.ConnectBlock(block)
}

func (vm *VM) DisconnectBlock(block consensus.Block) error {
	return vm.mchain.DisconnectBlock(block)
}

func (vm *VM) ParseBlock([]byte) (consensus.Block, error) {
	return nil, nil
}

func (vm *VM) LastAccepted() (*hash.Hash, error) {
	block := vm.chain.Ether().BlockChain().CurrentBlock()
	h := hash.MustBytesToHash(block.Hash().Bytes())
	return &h, nil
}

func (vm *VM) GetBalance(addre string) (int64, error) {
	addr, err := address.DecodeAddress(addre)
	if err != nil {
		return 0, err
	}
	secpPksAddr, ok := addr.(*address.SecpPubKeyAddress)
	if !ok {
		return 0, fmt.Errorf("Not SecpPubKeyAddress:%s", addr.String())
	}
	publicKey, err := crypto.UnmarshalPubkey(secpPksAddr.PubKey().SerializeUncompressed())
	if err != nil {
		return 0, err
	}
	eAddr := crypto.PubkeyToAddress(*publicKey)
	state, err := vm.chain.Ether().BlockChain().State()
	if err != nil {
		return 0, err
	}
	ba := state.GetBalance(eAddr)
	if ba == nil {
		return 0, fmt.Errorf("No balance for address %s", eAddr)
	}
	ba = ba.Div(ba, qcommon.Precision)
	return ba.Int64(), nil
}

func (vm *VM) VerifyTx(tx consensus.Tx) (int64, error) {
	if tx.GetTxType() == qtypes.TxTypeCrossChainVM {
		txb := common.FromHex(string(tx.GetData()))
		var txe = &types.Transaction{}
		if err := txe.UnmarshalBinary(txb); err != nil {
			return 0, fmt.Errorf("rlp decoding failed: %v", err)
		}
		err := vm.validateTx(txe)
		if err != nil {
			return 0, err
		}
		cost := txe.Cost()
		cost = cost.Sub(cost, txe.Value())
		cost = cost.Div(cost, qcommon.Precision)
		return cost.Int64(), nil
	}
	return 0, fmt.Errorf("Not support")
}

func (vm *VM) validateTx(tx *types.Transaction) error {
	if uint64(tx.Size()) > txMaxSize {
		return core.ErrOversizedData
	}
	if tx.Value().Sign() < 0 {
		return core.ErrNegativeValue
	}
	if tx.GasFeeCap().BitLen() > 256 {
		return core.ErrFeeCapVeryHigh
	}
	if tx.GasTipCap().BitLen() > 256 {
		return core.ErrTipVeryHigh
	}
	if tx.GasFeeCapIntCmp(tx.GasTipCap()) < 0 {
		return core.ErrTipAboveFeeCap
	}
	from, err := types.Sender(types.LatestSigner(vm.chain.Ether().BlockChain().Config()), tx)
	if err != nil {
		return core.ErrInvalidSender
	}
	currentState, err := vm.chain.Ether().BlockChain().State()
	if err != nil {
		return err
	}
	if currentState.GetNonce(from) > tx.Nonce() {
		return core.ErrNonceTooLow
	}
	if currentState.GetBalance(from).Cmp(tx.Cost()) < 0 {
		return core.ErrInsufficientFunds
	}
	intrGas, err := core.IntrinsicGas(tx.Data(), tx.AccessList(), tx.To() == nil, true, true)
	if err != nil {
		return err
	}
	if tx.Gas() < intrGas {
		return core.ErrIntrinsicGas
	}
	return nil
}

func (vm *VM) addTx(tx *types.Transaction) (*qtypes.Transaction, error) {
	txmb, err := tx.MarshalBinary()
	if err != nil {
		return nil, err
	}
	txmbHex := hexutil.Encode(txmb)

	qtxhb := tx.Hash().Bytes()
	qcommon.ReverseBytes(&qtxhb)
	qtxh := hash.MustBytesToHash(qtxhb)

	mtx := qtypes.NewTransaction()
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

	acceptedTxs, err := vm.ctx.GetTxPool().ProcessTransaction(qtypes.NewTx(mtx), false, false, true)
	if err != nil {
		return nil, err
	}
	vm.ctx.GetNotify().AnnounceNewTransactions(acceptedTxs, nil)
	vm.ctx.GetNotify().AddRebroadcastInventory(acceptedTxs)

	return mtx, nil
}

func (vm *VM) sendTxs(txs []*types.Transaction) {
	for _, tx := range txs {
		qtx, err := vm.addTx(tx)
		if err != nil {
			log.Error(fmt.Sprintf("Ignore evm tx(%s)[Exist in qng tx(%s)] from tx pool:%v", tx.Hash().String(), qtx.TxHash(), err.Error()))
			vm.chain.Ether().TxPool().RemoveTx(tx.Hash(), true)
		}
	}
}

func (vm *VM) initTxPool() {
	go func() {
		<-time.After(time.Second * 2)
		log.Debug("EVM:start init txpool")
		pending, err := vm.chain.Ether().TxPool().Pending(false)
		if err != nil {
			log.Error("Failed to fetch pending transactions", "err", err)
		} else {
			if len(pending) > 0 {
				for _, txs := range pending {
					vm.sendTxs(txs)
				}
			}
		}

	}()
}

func (vm *VM) handler() {
	log.Debug("Meerevm handler start")
	defer vm.txsSub.Unsubscribe()

out:
	for {
		select {

		case ev := <-vm.txsCh:
			vm.sendTxs(ev.Txs)

		case <-vm.shutdownChan:
			break out
		}
	}

cleanup:
	for {
		select {
		case <-vm.txsCh:
		default:
			break cleanup
		}
	}

	vm.shutdownWg.Done()
	log.Debug("Meerevm handler done")
}

func (vm *VM) RegisterAPIs(apis []api.API) {
	vm.mchain.RegisterAPIs(apis)
}

func New() *VM {
	return &VM{
		shutdownChan: make(chan struct{}),
		txsCh:        make(chan core.NewTxsEvent, 256),
	}
}
