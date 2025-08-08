package client

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/ethereum/go-ethereum/accounts/abi"
)

const (
	ERC20ABIPath = "./abi/erc20.json"
)

var (
	ERC20ABI *abi.ABI
)

func InitializeABIs() {
	parsedERC20ABI, err := loadABI(ERC20ABIPath)
	if err != nil {
		log.Fatalf("Error loading ERC20 ABI: %v", err)
	}
	ERC20ABI = &parsedERC20ABI
}

func loadABI(filePath string) (abi.ABI, error) {
	absPath, err := filepath.Abs(filePath)
	if err != nil {
		return abi.ABI{}, fmt.Errorf("failed to resolve ABI file path: %w", err)
	}

	file, err := os.Open(absPath)
	if err != nil {
		return abi.ABI{}, fmt.Errorf("failed to open ABI file: %w", err)
	}
	defer file.Close()

	data, err := io.ReadAll(file)
	if err != nil {
		return abi.ABI{}, fmt.Errorf("failed to read ABI file: %w", err)
	}

	parsedABI, err := abi.JSON(strings.NewReader(string(data)))
	if err != nil {
		return abi.ABI{}, fmt.Errorf("failed to parse ABI: %w", err)
	}

	return parsedABI, nil
}
