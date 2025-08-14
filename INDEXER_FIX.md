# Indexer Fix for ERC20/ERC721 Contamination Issue

## Problem
The indexer was incorrectly storing ERC20 approval events in the NFT tables because both ERC20 and ERC721 `Approval` events have the same signature: `Approval(address,address,uint256)`.

This caused ERC20 tokens like "Wrapped Kaspa" and "Blanco" to appear as ERC721 tokens in the frontend.

## Root Cause
In `main.go` lines 31-33:
```go
var approvalEventSignature = crypto.Keccak256Hash([]byte("Approval(address,address,uint256)"))
var nftApprovalEventSignature = crypto.Keccak256Hash([]byte("Approval(address,address,uint256)"))  // ❌ SAME SIGNATURE!
```

Both ERC20 and ERC721 approval events were being processed by both:
- `processApprovalEvents()` → stores in `erc20_approvals` table  
- `processNFTEvents()` → stores in `nft_approvals` table

## Solution Applied
Added contract type detection to prevent ERC20 approvals from being stored as NFT approvals:

### 1. Modified `processNFTEvents()` function
- Added `isNFTContract()` check before processing approval events as NFT events
- Only stores approvals in NFT tables if the contract is actually an NFT contract

### 2. Added `isNFTContract()` function
- Checks if contract is already known as ERC20 (early exit)
- Calls `supportsInterface()` to check for ERC721 interface support
- Falls back to testing NFT-specific methods (`tokenURI`, `ownerOf`)

### 3. Added `hasNFTMethods()` helper function
- Tests for NFT-specific functions as fallback detection
- Tries `tokenURI(uint256)` and `ownerOf(uint256)` calls

## Files Modified
- `main.go`: Added contract type detection logic

## Expected Result
- ERC20 approvals will only be stored in `erc20_approvals` table
- ERC721 approvals will only be stored in `nft_approvals` table  
- Frontend will correctly label ERC20 tokens as "ERC20" instead of "ERC721"

## Database Cleanup (Optional)
To clean existing contaminated data:
```sql
-- Remove ERC20 approvals from NFT tables (where contract is known ERC20)
DELETE FROM nft_approvals 
WHERE contract_address IN (
    SELECT address FROM erc20_tokens
);

DELETE FROM nft_operator_approvals 
WHERE contract_address IN (
    SELECT address FROM erc20_tokens  
);
```

## Next Steps
1. Restart the indexer with the updated code
2. Monitor logs to ensure proper filtering
3. Check frontend to verify ERC20 tokens are labeled correctly
