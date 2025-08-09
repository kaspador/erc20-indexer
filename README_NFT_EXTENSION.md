# NFT Indexing Extension

This document explains the NFT indexing extension added to the ERC20 Indexer.

## Overview

The indexer now monitors and indexes both ERC20 approvals and NFT (ERC721) approval events on the Kasplex blockchain, automatically creating and populating database tables as needed.

## New Database Tables

The indexer automatically creates these additional tables:

### `nft_approvals`
Stores individual NFT approvals (`approve(to, tokenId)` events).

| Column | Type | Description |
|--------|------|-------------|
| id | BIGSERIAL | Primary key |
| contract_address | VARCHAR(42) | NFT contract address |
| owner | VARCHAR(42) | NFT owner address |
| approved | VARCHAR(42) | Approved address for this specific NFT |
| token_id | VARCHAR(78) | Specific NFT token ID |
| block_number | BIGINT | Block where approval occurred |
| block_hash | VARCHAR(66) | Block hash |
| tx_hash | VARCHAR(66) | Transaction hash |
| created_at | TIMESTAMP | Record creation time |
| updated_at | TIMESTAMP | Record update time |

**Unique constraint**: `(contract_address, owner, token_id)`

### `nft_operator_approvals`
Stores operator approvals (`setApprovalForAll(operator, approved)` events).

| Column | Type | Description |
|--------|------|-------------|
| id | BIGSERIAL | Primary key |
| contract_address | VARCHAR(42) | NFT contract address |
| owner | VARCHAR(42) | NFT owner address |
| operator | VARCHAR(42) | Operator address (can manage all NFTs) |
| approved | BOOLEAN | Whether operator is approved |
| block_number | BIGINT | Block where approval occurred |
| block_hash | VARCHAR(66) | Block hash |
| tx_hash | VARCHAR(66) | Transaction hash |
| created_at | TIMESTAMP | Record creation time |
| updated_at | TIMESTAMP | Record update time |

**Unique constraint**: `(contract_address, owner, operator)`

## Events Monitored

The indexer now listens for these additional events:

### ERC721 Individual Approval
```solidity
event Approval(address indexed owner, address indexed approved, uint256 indexed tokenId);
```
- **Topic Hash**: `0x8c5be1e5ebec7d5bd14f71427d1e84f3dd0314c0f7b2291e5b200ac8c7c3b925`
- **Stores in**: `nft_approvals` table

### ERC721/ERC1155 Operator Approval
```solidity
event ApprovalForAll(address indexed owner, address indexed operator, bool approved);
```
- **Topic Hash**: `0x17307eab39ab6107e8899845ad3d59bd9653f200f220920489ca2b5937696c31`
- **Stores in**: `nft_operator_approvals` table

## New Functions Added

### Database Functions (`database/postgres.go`)
- `SaveOrUpdateNFTApproval(approval *NFTApproval) error`
- `SaveOrUpdateNFTOperatorApproval(approval *NFTOperatorApproval) error`
- `GetNFTApprovalsByOwner(owner string, offset, limit int) ([]NFTApproval, error)`
- `GetNFTOperatorApprovalsByOwner(owner string, offset, limit int) ([]NFTOperatorApproval, error)`

### Main Indexer Functions (`main.go`)
- `processNFTEvents(ethClient *ethclient.Client, blockNumber uint64, blockHash string) error`
- `processNFTApproval(vLog types.Log, blockNumber uint64, blockHash string) error`
- `processNFTOperatorApproval(vLog types.Log, blockNumber uint64, blockHash string) error`

## How It Works

1. **Automatic Table Creation**: When the indexer starts, it automatically creates the NFT tables if they don't exist
2. **Event Processing**: For each block, the indexer:
   - Processes ERC20 approval events (existing functionality)
   - Processes NFT approval events (new)
   - Processes NFT operator approval events (new)
3. **Data Updates**: Uses `ON CONFLICT` to update existing records when new events modify previous approvals
4. **Indexing**: Proper database indexes ensure fast queries by owner, contract, and block number

## Integration with Frontend

The frontend `/api/nft-approvals` and `/api/operator-approvals` endpoints will automatically use this data once the indexer populates the tables.

## Performance Impact

- **Minimal**: NFT event processing runs in parallel with existing ERC20 processing
- **Efficient**: Uses the same database connection and transaction patterns
- **Scalable**: Proper indexing ensures fast queries even with large datasets

## Running the Extended Indexer

No configuration changes needed. Simply run the indexer as before:

```bash
go run main.go
```

The indexer will:
1. Create NFT tables automatically on first run
2. Process historical blocks to index existing NFT approvals
3. Continue monitoring new blocks for both ERC20 and NFT events

## Monitoring

Watch the logs for NFT event processing:
```
2024/01/15 10:30:15 Processed 5 NFT events in block 12345
2024/01/15 10:30:16 Processed 12 approval events in block 12346
```

## Database Indexes

The following indexes are automatically created for optimal performance:

**NFT Approvals:**
- `idx_nft_approvals_owner` - Fast lookups by owner
- `idx_nft_approvals_contract` - Fast lookups by contract
- `idx_nft_approvals_block` - Fast lookups by block

**NFT Operator Approvals:**
- `idx_nft_operator_approvals_owner` - Fast lookups by owner
- `idx_nft_operator_approvals_contract` - Fast lookups by contract  
- `idx_nft_operator_approvals_block` - Fast lookups by block

This extension makes the indexer a comprehensive solution for both ERC20 and NFT approval tracking on Kasplex!
