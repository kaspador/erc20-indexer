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
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
)

type Indexer struct {
	state    *database.IndexerState
	stateMux sync.RWMutex
	stopChan chan struct{}
	wg       sync.WaitGroup
}

var approvalEventSignature = crypto.Keccak256Hash([]byte("Approval(address,address,uint256)"))
var nftApprovalEventSignature = crypto.Keccak256Hash([]byte("Approval(address,address,uint256)"))
var nftApprovalForAllEventSignature = crypto.Keccak256Hash([]byte("ApprovalForAll(address,address,bool)"))

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

func (i *Indexer) waitForETHClient() *ethclient.Client {
	maxRetries := 30 // Max 5 minutes of retries (30 * 10 seconds)
	retryCount := 0

	for retryCount < maxRetries {
		select {
		case <-i.stopChan:
			log.Println("Stop signal received while waiting for ETH client")
			return nil
		default:
		}

		ethClient := client.GetETHClient()
		if ethClient != nil {
			// Test the connection
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_, err := ethClient.BlockNumber(ctx)
			cancel()

			if err == nil {
				log.Println("ETH client connection established successfully")
				return ethClient
			}
			log.Printf("ETH client test failed: %v", err)
		}

		retryCount++
		log.Printf("Waiting for ETH client connection... (attempt %d/%d)", retryCount, maxRetries)
		time.Sleep(10 * time.Second)
	}

	log.Printf("Failed to establish ETH client connection after %d attempts", maxRetries)
	return nil
}

func (i *Indexer) indexBlocks() {
	defer i.wg.Done()

	// Wait for ETH client to be available with retry logic
	ethClient := i.waitForETHClient()
	if ethClient == nil {
		log.Println("Unable to establish ETH client connection, stopping indexer")
		return
	}

	latestBlock, err := ethClient.BlockNumber(context.Background())
	if err != nil {
		log.Printf("Failed to get latest block number: %v, will retry...", err)
		// Don't fatal here, let the monitoring loop handle reconnection
		time.Sleep(10 * time.Second)
		i.indexBlocks() // Retry the entire function
		return
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

	if err := i.processApprovalEvents(ethClient, blockNumber, block.Hash().Hex()); err != nil {
		log.Printf("Error processing approval events for block %d: %v", blockNumber, err)
	}

	if err := i.processNFTEvents(ethClient, blockNumber, block.Hash().Hex()); err != nil {
		log.Printf("Error processing NFT events for block %d: %v", blockNumber, err)
	}

	return nil
}

func (i *Indexer) processApprovalEvents(ethClient *ethclient.Client, blockNumber uint64, blockHash string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	query := ethereum.FilterQuery{
		FromBlock: big.NewInt(int64(blockNumber)),
		ToBlock:   big.NewInt(int64(blockNumber)),
		Topics: [][]common.Hash{
			{approvalEventSignature},
		},
	}

	logs, err := ethClient.FilterLogs(ctx, query)
	if err != nil {
		return fmt.Errorf("failed to filter logs for block %d: %w", blockNumber, err)
	}

	approvalCount := 0
	for _, vLog := range logs {
		if len(vLog.Topics) >= 3 {
			approval, err := i.parseApprovalEvent(vLog, blockNumber, blockHash)
			if err != nil {
				log.Printf("Error parsing approval event in tx %s: %v", vLog.TxHash.Hex(), err)
				continue
			}

			exists, err := database.TokenExists(approval.TokenAddress)
			if err != nil {
				log.Printf("Error checking if token exists %s: %v", approval.TokenAddress, err)
				continue
			}

			if !exists {
				tokenInfo, isERC20 := i.getERC20TokenInfo(ethClient, common.HexToAddress(approval.TokenAddress), blockNumber, blockHash, vLog.TxHash.Hex())
				if isERC20 {
					if err := i.addERC20Token(tokenInfo); err != nil {
						log.Printf("Error adding ERC20 token %s: %v", approval.TokenAddress, err)
						continue
					}
				} else {
					continue
				}
			}

			if err := database.SaveOrUpdateERC20Approval(approval); err != nil {
				log.Printf("Error saving approval for token %s: %v", approval.TokenAddress, err)
				continue
			}

			approvalCount++
		}
	}

	if approvalCount > 0 {
		log.Printf("Processed %d approval events in block %d", approvalCount, blockNumber)
	}

	return nil
}

func (i *Indexer) parseApprovalEvent(vLog types.Log, blockNumber uint64, blockHash string) (*database.ERC20Approval, error) {
	if len(vLog.Topics) < 3 {
		return nil, fmt.Errorf("insufficient topics for Approval event")
	}

	if len(vLog.Data) < 32 {
		return nil, fmt.Errorf("insufficient data for Approval event")
	}

	owner := common.HexToAddress(vLog.Topics[1].Hex()).Hex()
	spender := common.HexToAddress(vLog.Topics[2].Hex()).Hex()

	amount := new(big.Int).SetBytes(vLog.Data)

	approval := &database.ERC20Approval{
		TokenAddress: vLog.Address.Hex(),
		Owner:        owner,
		Spender:      spender,
		Amount:       amount.String(),
		BlockNumber:  blockNumber,
		BlockHash:    blockHash,
		TxHash:       vLog.TxHash.Hex(),
	}

	return approval, nil
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

func (i *Indexer) isNFTContract(ethClient *ethclient.Client, address common.Address) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Check if it's already known as an ERC20 token
	exists, err := database.TokenExists(address.Hex())
	if err == nil && exists {
		// It's a known ERC20 token, so it's not an NFT
		return false
	}

	// Try to call ERC721-specific functions to determine if it's an NFT
	// We'll try to call supportsInterface for ERC721 interface ID (0x80ac58cd)
	erc721InterfaceID := [4]byte{0x80, 0xac, 0x58, 0xcd}
	
	// Create supportsInterface call data
	// supportsInterface(bytes4) selector is 0x01ffc9a7
	callData := make([]byte, 36)
	copy(callData[0:4], []byte{0x01, 0xff, 0xc9, 0xa7}) // supportsInterface selector
	copy(callData[4:8], erc721InterfaceID[:])           // ERC721 interface ID

	msg := ethereum.CallMsg{
		To:   &address,
		Data: callData,
	}

	result, err := ethClient.CallContract(ctx, msg, nil)
	if err != nil || len(result) < 32 {
		// If supportsInterface fails, try other NFT-specific methods
		return i.hasNFTMethods(ethClient, address)
	}

	// Check if the result indicates ERC721 support
	// Result should be a boolean (true = supports ERC721)
	if len(result) >= 32 && result[31] == 1 {
		return true
	}

	// Fallback to checking for NFT-specific methods
	return i.hasNFTMethods(ethClient, address)
}

func (i *Indexer) hasNFTMethods(ethClient *ethclient.Client, address common.Address) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Try to call tokenURI function which is specific to NFTs
	// tokenURI(uint256) selector is 0xc87b56dd
	callData := make([]byte, 36)
	copy(callData[0:4], []byte{0xc8, 0x7b, 0x56, 0xdd}) // tokenURI selector
	// Use token ID 1 as test
	copy(callData[4:36], make([]byte, 32)) // token ID 1 (padded to 32 bytes)
	callData[35] = 1

	msg := ethereum.CallMsg{
		To:   &address,
		Data: callData,
	}

	_, err := ethClient.CallContract(ctx, msg, nil)
	if err == nil {
		// tokenURI call succeeded, likely an NFT
		return true
	}

	// Try ownerOf function which is also specific to NFTs
	// ownerOf(uint256) selector is 0x6352211e
	callData2 := make([]byte, 36)
	copy(callData2[0:4], []byte{0x63, 0x52, 0x21, 0x1e}) // ownerOf selector
	copy(callData2[4:36], make([]byte, 32))               // token ID 1 (padded to 32 bytes)
	callData2[35] = 1

	msg2 := ethereum.CallMsg{
		To:   &address,
		Data: callData2,
	}

	_, err = ethClient.CallContract(ctx, msg2, nil)
	return err == nil // If ownerOf succeeds, it's likely an NFT
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
				log.Println("ETH client not available during monitoring, waiting for reconnection...")
				continue
			}

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			latestBlock, err := ethClient.BlockNumber(ctx)
			cancel()

			if err != nil {
				log.Printf("Failed to get latest block during monitoring: %v", err)
				// Force a reconnection attempt
				client.ForceReconnectETH()
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

func (i *Indexer) processNFTEvents(ethClient *ethclient.Client, blockNumber uint64, blockHash string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Process both NFT Approval and ApprovalForAll events
	query := ethereum.FilterQuery{
		FromBlock: big.NewInt(int64(blockNumber)),
		ToBlock:   big.NewInt(int64(blockNumber)),
		Topics: [][]common.Hash{
			{nftApprovalEventSignature, nftApprovalForAllEventSignature},
		},
	}

	logs, err := ethClient.FilterLogs(ctx, query)
	if err != nil {
		return fmt.Errorf("failed to filter NFT logs for block %d: %w", blockNumber, err)
	}

	nftEventCount := 0
	for _, vLog := range logs {
		if len(vLog.Topics) >= 3 {
			if vLog.Topics[0] == nftApprovalEventSignature {
				// Check if this is actually an NFT contract before processing as NFT approval
				if i.isNFTContract(ethClient, vLog.Address) {
					// Handle individual NFT approval
					if err := i.processNFTApproval(vLog, blockNumber, blockHash); err != nil {
						log.Printf("Error processing NFT approval in tx %s: %v", vLog.TxHash.Hex(), err)
						continue
					}
					nftEventCount++
				}
				// If it's not an NFT contract, skip processing as NFT (it's likely ERC20)
			} else if vLog.Topics[0] == nftApprovalForAllEventSignature {
				// ApprovalForAll is specific to NFTs, so we can process it directly
				// But let's still verify it's an NFT contract for safety
				if i.isNFTContract(ethClient, vLog.Address) {
					// Handle operator approval (setApprovalForAll)
					if err := i.processNFTOperatorApproval(vLog, blockNumber, blockHash); err != nil {
						log.Printf("Error processing NFT operator approval in tx %s: %v", vLog.TxHash.Hex(), err)
						continue
					}
					nftEventCount++
				}
			}
		}
	}

	if nftEventCount > 0 {
		log.Printf("Processed %d NFT events in block %d", nftEventCount, blockNumber)
	}

	return nil
}

func (i *Indexer) processNFTApproval(vLog types.Log, blockNumber uint64, blockHash string) error {
	if len(vLog.Topics) < 3 {
		return fmt.Errorf("insufficient topics for NFT Approval event")
	}

	if len(vLog.Data) < 32 {
		return fmt.Errorf("insufficient data for NFT Approval event")
	}

	owner := common.HexToAddress(vLog.Topics[1].Hex()).Hex()
	approved := common.HexToAddress(vLog.Topics[2].Hex()).Hex()
	tokenID := new(big.Int).SetBytes(vLog.Data).String()

	nftApproval := &database.NFTApproval{
		ContractAddress: vLog.Address.Hex(),
		Owner:           owner,
		Approved:        approved,
		TokenID:         tokenID,
		BlockNumber:     blockNumber,
		BlockHash:       blockHash,
		TxHash:          vLog.TxHash.Hex(),
	}

	if err := database.SaveOrUpdateNFTApproval(nftApproval); err != nil {
		return fmt.Errorf("failed to save NFT approval: %w", err)
	}

	return nil
}

func (i *Indexer) processNFTOperatorApproval(vLog types.Log, blockNumber uint64, blockHash string) error {
	if len(vLog.Topics) < 3 {
		return fmt.Errorf("insufficient topics for NFT ApprovalForAll event")
	}

	if len(vLog.Data) < 32 {
		return fmt.Errorf("insufficient data for NFT ApprovalForAll event")
	}

	owner := common.HexToAddress(vLog.Topics[1].Hex()).Hex()
	operator := common.HexToAddress(vLog.Topics[2].Hex()).Hex()
	approved := len(vLog.Data) > 0 && vLog.Data[31] == 1 // Last byte indicates true/false

	operatorApproval := &database.NFTOperatorApproval{
		ContractAddress: vLog.Address.Hex(),
		Owner:           owner,
		Operator:        operator,
		Approved:        approved,
		BlockNumber:     blockNumber,
		BlockHash:       blockHash,
		TxHash:          vLog.TxHash.Hex(),
	}

	if err := database.SaveOrUpdateNFTOperatorApproval(operatorApproval); err != nil {
		return fmt.Errorf("failed to save NFT operator approval: %w", err)
	}

	return nil
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
