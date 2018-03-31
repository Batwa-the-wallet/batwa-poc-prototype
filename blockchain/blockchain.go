package blockchain

import (
	"encoding/hex"
	"errors"
	"math/big"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/champii/go-dht/dht"
	logging "github.com/op/go-logging"
	"github.com/vmihailenco/msgpack"
)

const (
	COMMAND_CUSTOM_GET_INFO = iota
	COMMAND_CUSTOM_NEW_TRANSACTION
	COMMAND_CUSTOM_NEW_BLOCK
)

var EXPECTED_10_BLOCKS_TIME int64 = 600

type UnspentTxOut struct {
	Out        TxOut
	TxHash     []byte
	InIdx      int
	IsTargeted bool
}

type HistoryTx struct {
	Address   string `json:"address"`
	Timestamp int64  `json:"timestamp"`
	Amount    int    `json:"amount"`
}

type Blockchain struct {
	sync.RWMutex
	client              *dht.Dht
	logger              *logging.Logger
	options             BlockchainOptions
	headers             []BlockHeader
	baseTarget          []byte
	lastTarget          []byte
	wallets             map[string]*Wallet
	unspentTxOut        map[string][]UnspentTxOut
	pendingTransactions []Transaction
	miningBlock         *Block
	synced              bool
	mustStop            bool
	stats               *Stats
	running             bool
	history             []HistoryTx
}

type BlockchainOptions struct {
	BootstrapAddr string
	ListenAddr    string
	Folder        string
	Send          string
	Interactif    bool
	Wallets       bool
	Stats         bool
	Verbose       int
	Mine          bool
	NoGui         bool
	Cluster       int
}

func New(options BlockchainOptions) *Blockchain {
	target, _ := hex.DecodeString("000000FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFF")
	// target, _ := hex.DecodeString("000000FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFF")

	if options.Stats {
		options.Verbose = 2
	}

	bc := &Blockchain{
		options:             options,
		baseTarget:          target,
		lastTarget:          target,
		wallets:             make(map[string]*Wallet),
		unspentTxOut:        make(map[string][]UnspentTxOut),
		mustStop:            false,
		stats:               &Stats{},
		pendingTransactions: []Transaction{},
	}

	bc.Init()

	return bc
}

func (this *Blockchain) Init() {
	client := dht.New(dht.DhtOptions{
		ListenAddr:    this.options.ListenAddr,
		BootstrapAddr: this.options.BootstrapAddr,
		Verbose:       this.options.Verbose,
		OnStore: func(pack dht.Packet) bool {
			this.Lock()
			defer this.Unlock()
			block := Block{}

			// var cmd dht.StoreInst

			cmd := pack.GetStore()

			// pack.

			// if err != nil {
			// 	this.logger.Critical("ONSTORE Get data", err.Error())

			// 	return false
			// }

			err := msgpack.Unmarshal(cmd.Header.Data, &block)

			if err != nil {
				this.logger.Critical("ONSTORE Unmarshal error", err.Error())

				return false
			}

			if block.Header.Height == this.headers[len(this.headers)-1].Height+1 {
				return block.Verify(this)
			} else if block.Header.Height <= this.headers[len(this.headers)-1].Height {
				return block.VerifyOld(this)
			} else {
				return false
			}
		},

		OnCustomCmd: func(cmd dht.Packet) interface{} {
			// FIXME

			// return this.Dispatch(cmd)
			return nil
		},

		OnBroadcast: func(packet dht.Packet) interface{} {
			blob := packet.GetBroadcast().Data

			var cmd dht.Custom

			err := msgpack.Unmarshal(blob, &cmd)

			if err != nil {
				this.logger.Error("Error on OnBroadcast", err)

				return nil
			}

			// packet.Data = &cmd

			if this.synced {
				return this.Dispatch(&cmd)
			}

			return nil
		},
	})

	this.client = client
	this.logger = client.Logger()

	if err := SetupStorage(this); err != nil {
		this.logger.Critical(err)

		return
	}

	OriginBlock(this)

	this.headers = append(this.headers, originalBlock.Header)
	this.miningBlock = originalBlock

	if err := LoadStoredHeaders(this); err != nil {
		this.logger.Critical("Cannot load stored headers", err)

		return
	}

	if err := LoadUnspent(this); err != nil {
		this.logger.Critical("Cannot load unspent tx", err)

		return
	}

}

func (this *Blockchain) Stop() {
	this.client.Stop()
	StoreLastHeaders(this)
	StoreUnspent(this)
}

func (this *Blockchain) Start() error {
	if err := this.client.Start(); err != nil {
		return err
	}

	if this.options.Stats {
		go this.StatsLoop()
	}

	go func() {
		this.Sync()

		if !this.synced {
			this.logger.Error("Unable to sync")

			return
		}

		if this.options.Wallets {
			this.ShowWallets()

		}

		if len(this.options.Send) > 0 {
			if err := this.SendTo(this.options.Send); err != nil {
				this.logger.Error("Unable to Send", err)

				return
			}

			time.Sleep(time.Second * 5)
			os.Exit(0)
		}

		if this.options.Mine {
			this.Mine()
		}
	}()

	return nil
}

func (this *Blockchain) SendTo(value string) error {
	splited := strings.Split(value, ":")

	if len(splited) != 2 {
		return errors.New("Bad send format")
	}

	amount, err := strconv.Atoi(splited[0])

	if err != nil || amount <= 0 {
		return errors.New("Invalid amount: " + splited[0])
	}

	// pub := UnsanitizePubKey(splited[1])

	tx := NewTransaction(amount, []byte(splited[1]), this)

	if tx == nil || !this.AddTransationToWaiting(tx) {
		return errors.New("Unable to create the transaction")
	}

	this.mustStop = true

	serie, err := msgpack.Marshal(tx)

	if err != nil {
		return errors.New("Cannot marshal transaction: " + err.Error())
	}

	this.client.Broadcast(dht.Custom{
		Command: COMMAND_CUSTOM_NEW_TRANSACTION,
		Data:    serie,
	})

	return nil
}

func (this *Blockchain) Logger() *logging.Logger {
	return this.logger
}

func (this *Blockchain) hasPending(tx *Transaction) bool {
	for _, t := range this.pendingTransactions {
		if compare(t.Stamp.Hash, tx.Stamp.Hash) == 0 {
			return true
		}
	}

	return false
}

func (this *Blockchain) Dispatch(cmd *dht.Custom) interface{} {
	// var cmd dht.CustomCmd
	// pack.GetData(&cmd)

	switch cmd.Command {
	case COMMAND_CUSTOM_NEW_TRANSACTION:
		var tx Transaction

		msgpack.Unmarshal(cmd.Data, &tx)

		if !this.AddTransationToWaiting(&tx) {
			return nil
		}

	case COMMAND_CUSTOM_NEW_BLOCK:
	}

	return nil
}

func (this *Blockchain) Wait() {
	this.client.Wait()
}

func (this *Blockchain) doSync() error {
	blob, err := this.client.Fetch(NewHash(this.headers[len(this.headers)-1].Hash))

	if err != nil {
		return err
	}

	var block Block

	msgpack.Unmarshal(blob, &block)

	if !this.AddBlock(&block) {
		this.logger.Warning("Sync: Received bad block")

		return errors.New("Cannot add block")
	}

	return nil
}

func (this *Blockchain) Sync() {
	this.logger.Info("Start syncing at", len(this.headers)-1)

	for this.doSync() == nil {
	}

	this.synced = true

	go func() {
		for {
			if err := this.doSync(); err != nil {
				time.Sleep(time.Second * 5)

				continue
			}

			this.mustStop = true

			time.Sleep(time.Second * 5)
		}
	}()
}

func (this *Blockchain) AddBlock(block *Block) bool {
	if !block.Verify(this) {
		this.logger.Error("Cannot add block: bad block")

		return false
	}

	this.Lock()
	this.headers = append(this.headers, block.Header)
	if err := StoreLastHeaders(this); err != nil {
		this.logger.Warning("Cannot store last headers", err)
	}
	this.Unlock()

	this.UpdateUnspentTxOuts(block)
	this.RemovePendingTransaction(block.Transactions)

	if block.Header.Height%10 == 0 {
		this.adjustDifficulty(block)
	}

	if err := StoreUnspent(this); err != nil {
		this.logger.Warning("Cannot store unspents", err)
	}

	return true
}

func (this *Blockchain) adjustDifficulty(block *Block) {
	base := big.NewInt(0)
	actual := big.NewInt(0)
	base.SetString(hex.EncodeToString(this.baseTarget), 16)
	actual.SetString(hex.EncodeToString(block.Header.Target), 16)

	oldDiff := big.NewInt(0)
	oldDiff = oldDiff.Quo(base, actual)

	timePassed := block.Header.Timestamp - this.headers[block.Header.Height-10].Timestamp

	newDiff := big.NewInt(0)
	newDiff = newDiff.Mul(oldDiff, big.NewInt(EXPECTED_10_BLOCKS_TIME/timePassed))

	test := big.NewInt(0)
	if newDiff.Int64() > test.Mul(oldDiff, big.NewInt(4)).Int64() {
		newDiff = test
	}

	test = big.NewInt(0)
	if newDiff.Int64() < test.Quo(oldDiff, big.NewInt(4)).Int64() {
		newDiff = test
	}

	if newDiff.Int64() < 1 {
		newDiff = big.NewInt(1)
	}

	test = big.NewInt(0)
	this.lastTarget = test.Quo(base, newDiff).Bytes()

	for len(this.lastTarget) < len(this.baseTarget) {
		this.lastTarget = append([]byte{0}, this.lastTarget...)
	}
}

func (this *Blockchain) Mine() {
	ticker := time.NewTicker(time.Second)

	go func() {
		for range ticker.C {
			this.stats.Update()
		}
	}()

	this.running = true

	go func() {
		for this.running {
			this.miningBlock = NewBlock(this)

			this.miningBlock.Mine(this.stats, &this.mustStop)

			if this.mustStop {
				this.mustStop = false

				ticker.Stop()
				this.Mine()
				return
			}

			this.logger.Info("Found block !", hex.EncodeToString(this.miningBlock.Header.Hash))

			serie, _ := msgpack.Marshal(this.miningBlock)

			_, nb, err := this.client.StoreAt(NewHash(this.headers[len(this.headers)-1].Hash), serie)

			if err != nil || nb == 0 {
				this.logger.Warning("ERROR STORING BLOCK IN THE DHT !", hex.EncodeToString(this.miningBlock.Header.Hash))

				continue

			}

			this.stats.foundBlocks++

			this.doSync()
		}
	}()
}

func (this *Blockchain) AreHeadersGood() bool {
	lastHeaderHash := this.headers[0].Hash

	for _, header := range this.headers[1:] {
		if compare(header.PrecHash, lastHeaderHash) != 0 {
			return false
		}

		lastHeaderHash = header.Hash
	}

	return true
}

func (this *Blockchain) Wallets() map[string]*Wallet {
	return this.wallets
}

func (this *Blockchain) Synced() bool {
	return this.synced
}

func (this *Blockchain) Running() bool {
	return this.running
}

func (this *Blockchain) Stats() *Stats {
	return this.stats
}

func (this *Blockchain) GetConnectedNodesNb() int {
	return this.client.GetConnectedNumber()
}

func (this *Blockchain) BlocksHeight() int64 {
	return this.headers[len(this.headers)-1].Height
}

func (this *Blockchain) TimeSinceLastBlock() int64 {
	return time.Now().Unix() - this.headers[len(this.headers)-1].Timestamp
}

func (this *Blockchain) StoredKeys() int {
	return this.client.StoredKeys()
}

func (this *Blockchain) WaitingTransactionCount() int {
	return len(this.pendingTransactions)
}

func (this *Blockchain) GetOwnHistory() []HistoryTx {
	return this.history
}

func (this *Blockchain) GetOwnWaitingTx() []HistoryTx {
	res := []HistoryTx{}

	for _, tx := range this.pendingTransactions {
		txValue := 0

		own := false

		addr := SanitizePubKey(tx.Stamp.Pub)
		ownAddrStr := []byte(SanitizePubKey(this.wallets["main.key"].pub))

		if compare(tx.Stamp.Pub, this.wallets["main.key"].pub) == 0 {
			own = true
		}

		for _, out := range tx.Outs {
			if own && compare(out.Address, ownAddrStr) != 0 {
				txValue -= out.Value
				addr = string(out.Address)
			}

			if !own && compare(out.Address, ownAddrStr) == 0 {
				txValue += out.Value
			}

			if len(tx.Ins) == 0 && len(tx.Outs) == 1 {
				txValue = 0
			}
		}

		if txValue != 0 {
			res = append(res, HistoryTx{
				Address:   addr,
				Timestamp: time.Now().Unix(),
				Amount:    txValue,
			})
		}
	}

	return res
}

func (this *Blockchain) ProcessingTransactionCount() int {
	if !this.running {
		return 0
	}

	return len(this.miningBlock.Transactions) - 1
}

func (this *Blockchain) Difficulty() int64 {
	base := big.NewInt(0)
	actual := big.NewInt(0)
	base.SetString(hex.EncodeToString(this.baseTarget), 16)
	actual.SetString(hex.EncodeToString(this.lastTarget), 16)

	return base.Quo(base, actual).Int64()
}

func (this *Blockchain) NextDifficulty() int64 {
	base := big.NewInt(0)
	actual := big.NewInt(0)
	base.SetString(hex.EncodeToString(this.baseTarget), 16)
	actual.SetString(hex.EncodeToString(this.lastTarget), 16)

	oldDiff := big.NewInt(0)
	oldDiff = oldDiff.Quo(base, actual)

	timePassed := (time.Now().Unix() - this.headers[len(this.headers)-1].Timestamp)

	if timePassed == 0 {
		return oldDiff.Int64()
	}

	nbBlocks := int64((len(this.headers) - 1) % 10)

	if nbBlocks == 0 {
		nbBlocks = 1
	}

	timePassed = (timePassed / nbBlocks) * 10

	if timePassed == 0 {
		timePassed = 1
	}

	newDiff := big.NewInt(0)
	newDiff = newDiff.Mul(oldDiff, big.NewInt((EXPECTED_10_BLOCKS_TIME / timePassed)))

	test := big.NewInt(0)
	if newDiff.Int64() > test.Mul(oldDiff, big.NewInt(4)).Int64() {
		newDiff = test
	}

	test = big.NewInt(0)
	if newDiff.Int64() < test.Quo(oldDiff, big.NewInt(4)).Int64() {
		newDiff = test
	}

	if newDiff.Int64() < 1 {
		newDiff = big.NewInt(1)
	}

	return newDiff.Int64()
}
