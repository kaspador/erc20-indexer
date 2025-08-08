package client

import (
	"context"
	"erc20-indexer/config"
	"log"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/ethclient"
)

var (
	ETHClient        *ethclient.Client
	WSETHClient      *ethclient.Client
	ethClientMutex   sync.RWMutex
	wsEthClientMutex sync.RWMutex
	isEthConnected   bool
	isWsConnected    bool
)

func InitializeClients() {
	if err := connectETHClient(); err != nil {
		log.Printf("Initial ETH client connection failed: %v. Will retry...", err)
	}

	if err := connectWSETHClient(); err != nil {
		log.Printf("Initial WebSocket client connection failed: %v. Will retry...", err)
	}

	go monitorETHConnection()
	go monitorWSConnection()
}

func connectETHClient() error {
	client, err := ethclient.Dial(config.GlobalAppConfig.RPC)
	if err != nil {
		return err
	}

	_, err = client.BlockNumber(context.Background())
	if err != nil {
		client.Close()
		return err
	}

	ethClientMutex.Lock()
	defer ethClientMutex.Unlock()

	if ETHClient != nil {
		ETHClient.Close()
	}

	ETHClient = client
	isEthConnected = true
	return nil
}

func connectWSETHClient() error {
	client, err := ethclient.Dial(config.GlobalAppConfig.WSRPC)
	if err != nil {
		return err
	}

	_, err = client.BlockNumber(context.Background())
	if err != nil {
		client.Close()
		return err
	}

	wsEthClientMutex.Lock()
	defer wsEthClientMutex.Unlock()

	if WSETHClient != nil {
		WSETHClient.Close()
	}

	WSETHClient = client
	isWsConnected = true
	return nil
}

func monitorETHConnection() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		<-ticker.C

		ethClientMutex.RLock()
		client := ETHClient
		connected := isEthConnected
		ethClientMutex.RUnlock()

		if client == nil || !connected {
			log.Println("ETH client disconnected, attempting to reconnect...")
			if err := connectETHClient(); err != nil {
				log.Printf("Failed to reconnect ETH client: %v", err)
				ethClientMutex.Lock()
				isEthConnected = false
				ethClientMutex.Unlock()
			}
			continue
		}

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_, err := client.BlockNumber(ctx)
		cancel()

		if err != nil {
			log.Printf("ETH client connection test failed: %v", err)
			ethClientMutex.Lock()
			isEthConnected = false
			ethClientMutex.Unlock()
			if err := connectETHClient(); err != nil {
				log.Printf("Failed to reconnect ETH client: %v", err)
			}
		}
	}
}

func monitorWSConnection() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		<-ticker.C

		wsEthClientMutex.RLock()
		client := WSETHClient
		connected := isWsConnected
		wsEthClientMutex.RUnlock()

		if client == nil || !connected {
			log.Println("WebSocket ETH client disconnected, attempting to reconnect...")
			if err := connectWSETHClient(); err != nil {
				log.Printf("Failed to reconnect WebSocket ETH client: %v", err)
				wsEthClientMutex.Lock()
				isWsConnected = false
				wsEthClientMutex.Unlock()
			}
			continue
		}

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_, err := client.BlockNumber(ctx)
		cancel()

		if err != nil {
			log.Printf("WebSocket ETH client connection test failed: %v", err)
			wsEthClientMutex.Lock()
			isWsConnected = false
			wsEthClientMutex.Unlock()
			if err := connectWSETHClient(); err != nil {
				log.Printf("Failed to reconnect WebSocket ETH client: %v", err)
			}
		}
	}
}

func GetETHClient() *ethclient.Client {
	ethClientMutex.RLock()
	defer ethClientMutex.RUnlock()
	return ETHClient
}

func GetWSETHClient() *ethclient.Client {
	wsEthClientMutex.RLock()
	defer wsEthClientMutex.RUnlock()
	return WSETHClient
}

func ForceReconnectETH() {
	log.Println("Forcing ETH client reconnection...")
	if err := connectETHClient(); err != nil {
		log.Printf("Forced ETH client reconnection failed: %v", err)
	}
}

func ForceReconnectWS() {
	log.Println("Forcing WebSocket ETH client reconnection...")
	if err := connectWSETHClient(); err != nil {
		log.Printf("Forced WebSocket ETH client reconnection failed: %v", err)
	}
}
