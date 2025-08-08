package main

import (
	"context"
	"erc20-indexer/client"
	"erc20-indexer/config"
	"erc20-indexer/database"
	"fmt"
	"log"
	"math/big"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
)

type Indexer struct {
	state    *database.IndexerState
	stateMux sync.RWMutex
	stopChan chan struct{}
	wg       sync.WaitGroup
}

func main() {
	config.Load()
	client.InitializeClients()
	client.InitializeABIs()

	if err := database.Initialize(); err != nil {
		log.Fatalf("Failed to initialize db: %v", err)
	}
	defer database.Close()

	indexer := NewIndexer()

	go indexer.handleShutdown()

	indexer.Start()
}

func NewIndexer() *Indexer {
	indexer := &Indexer{
		stopChan: make(chan struct{}),
	}

	indexer.loadState()

	return indexer
}

func (i *Indexer) loadState() {
	i.stateMux.Lock()
	defer i.stateMux.Unlock()

	state, err := database.LoadIndexerState()
	if err != nil {
		log.Printf("Error loading state from database: %v", err)
		i.state = &database.IndexerState{
			LastProcessedBlock: 0,
			TotalContracts:     0,
			LastUpdated:        time.Now(),
		}
		return
	}

	i.state = state
	log.Printf("Loaded state from database: Last block %d, Total contracts: %d",
		i.state.LastProcessedBlock, i.state.TotalContracts)
}

func (i *Indexer) saveState() error {
	i.stateMux.Lock()
	defer i.stateMux.Unlock()

	count, err := database.GetTotalERC20Count()
	if err != nil {
		log.Printf("Error getting total ERC20 count: %v", err)
	} else {
		i.state.TotalContracts = count
	}

	i.state.LastUpdated = time.Now()

	return database.SaveIndexerState(i.state)
}

func (i *Indexer) addERC20Token(token *database.ERC20Token) error {
	exists, err := database.TokenExists(token.Address)
	if err != nil {
		return fmt.Errorf("failed to check if token exists: %w", err)
	}

	if exists {
		log.Printf("ERC20 token %s already indexed", token.Address)
		return nil
	}

	if err := database.SaveERC20Token(token); err != nil {
		return fmt.Errorf("failed to save ERC20 token: %w", err)
	}

	i.stateMux.Lock()
	i.state.TotalContracts++
	i.stateMux.Unlock()

	log.Printf("Added ERC20 contract: %s (%s) at block %d (Total: %d)",
		token.Address, token.Symbol, token.BlockNumber, i.state.TotalContracts)

	return nil
}

func (i *Indexer) Start() {
	log.Println("Starting blockchain indexing...")

	i.wg.Add(1)
	go i.indexBlocks()

	i.wg.Wait()
	log.Println("Indexer stopped")
}

func (i *Indexer) indexBlocks() {
	defer i.wg.Done()

	ethClient := client.GetETHClient()
	if ethClient == nil {
		log.Fatal("ETH client not available")
	}

	latestBlock, err := ethClient.BlockNumber(context.Background())
	if err != nil {
		log.Fatalf("Failed to get latest block number: %v", err)
	}

	startBlock := i.state.LastProcessedBlock + 1
	if startBlock == 1 {
		startBlock = 0
	}

	log.Printf("Starting indexing from block %d to %d", startBlock, latestBlock)

	if startBlock <= latestBlock {
		i.processBlockRange(startBlock, latestBlock)
	}

	i.monitorNewBlocks()
}

func (i *Indexer) processBlockRange(startBlock, endBlock uint64) {
	ethClient := client.GetETHClient()
	saveInterval := uint64(1000)

	for blockNum := startBlock; blockNum <= endBlock; {
		select {
		case <-i.stopChan:
			log.Println("Stopping block processing...")
			return
		default:
		}

		batchEnd := blockNum + uint64(config.GlobalAppConfig.BatchSize) - 1
		if batchEnd > endBlock {
			batchEnd = endBlock
		}

		log.Printf("Processing blocks %d to %d (%.2f%%)",
			blockNum, batchEnd, float64(blockNum-startBlock)/float64(endBlock-startBlock)*100)

		for b := blockNum; b <= batchEnd; b++ {
			if err := i.processBlock(ethClient, b); err != nil {
				log.Printf("Error processing block %d: %v. Retrying...", b, err)
				time.Sleep(1 * time.Second)
				continue
			}

			i.stateMux.Lock()
			i.state.LastProcessedBlock = b
			i.stateMux.Unlock()

			if b%saveInterval == 0 || b == batchEnd {
				if err := i.saveState(); err != nil {
					log.Printf("Error saving state: %v", err)
				}
			}
		}

		blockNum = batchEnd + 1
		time.Sleep(200 * time.Millisecond)
	}

	log.Printf("Finished processing historical blocks. Total ERC20 contracts found: %d", i.state.TotalContracts)
}

func (i *Indexer) processBlock(ethClient *ethclient.Client, blockNumber uint64) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	block, err := ethClient.BlockByNumber(ctx, big.NewInt(int64(blockNumber)))
	if err != nil {
		return fmt.Errorf("failed to get block %d: %w", blockNumber, err)
	}

	for _, tx := range block.Transactions() {
		if tx.To() == nil {
			receipt, err := ethClient.TransactionReceipt(ctx, tx.Hash())
			if err != nil {
				log.Printf("Failed to get receipt for tx %s: %v", tx.Hash().Hex(), err)
				continue
			}

			if receipt.ContractAddress != (common.Address{}) {
				exists, err := database.TokenExists(receipt.ContractAddress.Hex())
				if err != nil {
					log.Printf("Error checking if token exists: %v", err)
					continue
				}

				if exists {
					continue
				}

				tokenInfo, isERC20 := i.getERC20TokenInfo(ethClient, receipt.ContractAddress, blockNumber, block.Hash().Hex(), tx.Hash().Hex())
				if isERC20 {
					if err := i.addERC20Token(tokenInfo); err != nil {
						log.Printf("Error adding ERC20 token %s: %v", receipt.ContractAddress.Hex(), err)
					}
				}
			}
		}
	}

	return nil
}

func (i *Indexer) getERC20TokenInfo(ethClient *ethclient.Client, address common.Address, blockNumber uint64, blockHash, txHash string) (*database.ERC20Token, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	code, err := ethClient.CodeAt(ctx, address, nil)
	if err != nil || len(code) == 0 {
		return nil, false
	}

	token := &database.ERC20Token{
		Address:     address.Hex(),
		BlockNumber: blockNumber,
		BlockHash:   blockHash,
		TxHash:      txHash,
	}

	erc20Methods := []string{"name", "symbol", "decimals", "totalSupply"}
	methodResults := make(map[string]interface{})

	for _, methodName := range erc20Methods {
		_, exists := client.ERC20ABI.Methods[methodName]
		if !exists {
			return nil, false
		}

		callData, err := client.ERC20ABI.Pack(methodName)
		if err != nil {
			return nil, false
		}

		msg := ethereum.CallMsg{
			To:   &address,
			Data: callData,
		}

		result, err := ethClient.CallContract(ctx, msg, nil)
		if err != nil || len(result) == 0 {
			return nil, false
		}

		unpacked, err := client.ERC20ABI.Unpack(methodName, result)
		if err != nil || len(unpacked) == 0 {
			return nil, false
		}

		methodResults[methodName] = unpacked[0]
	}

	if name, ok := methodResults["name"].(string); ok {
		token.Name = name
	}

	if symbol, ok := methodResults["symbol"].(string); ok {
		token.Symbol = symbol
	}

	if decimals, ok := methodResults["decimals"].(uint8); ok {
		token.Decimals = decimals
	}

	if totalSupply, ok := methodResults["totalSupply"].(*big.Int); ok {
		token.TotalSupply = totalSupply.String()
	}

	if token.Name == "" && token.Symbol == "" {
		return nil, false
	}

	return token, true
}

func (i *Indexer) monitorNewBlocks() {
	log.Println("Started to monitor new blocks...")
	ticker := time.NewTicker(12 * time.Second)
	defer ticker.Stop()

	var lastCheckedBlock uint64 = i.state.LastProcessedBlock

	for {
		select {
		case <-i.stopChan:
			return
		case <-ticker.C:
			ethClient := client.GetETHClient()
			if ethClient == nil {
				log.Println("ETH client not available, retrying...")
				time.Sleep(5 * time.Second)
				continue
			}

			latestBlock, err := ethClient.BlockNumber(context.Background())
			if err != nil {
				log.Printf("Failed to get latest block: %v", err)
				continue
			}

			if latestBlock > lastCheckedBlock {
				log.Printf("New blocks detected: %d to %d", lastCheckedBlock+1, latestBlock)
				i.processBlockRange(lastCheckedBlock+1, latestBlock)
				lastCheckedBlock = latestBlock
			}
		}
	}
}

func (i *Indexer) handleShutdown() {
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	<-sigChan
	log.Println("Shutdown signal received, saving state...")

	close(i.stopChan)

	if err := i.saveState(); err != nil {
		log.Printf("Error saving final state: %v", err)
	}

	log.Println("Graceful shutdown completed")
	os.Exit(0)
}
