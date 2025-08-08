package database

import (
	"context"
	"database/sql"
	"erc20-indexer/config"
	"fmt"
	"log"
	"time"

	_ "github.com/lib/pq"
)

var db *sql.DB

type ERC20Token struct {
	ID          int64     `json:"id"`
	Address     string    `json:"address"`
	Name        string    `json:"name"`
	Symbol      string    `json:"symbol"`
	Decimals    uint8     `json:"decimals"`
	TotalSupply string    `json:"total_supply"`
	BlockNumber uint64    `json:"block_number"`
	BlockHash   string    `json:"block_hash"`
	TxHash      string    `json:"tx_hash"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type IndexerState struct {
	ID                 int       `json:"id"`
	LastProcessedBlock uint64    `json:"last_processed_block"`
	TotalContracts     uint64    `json:"total_contracts"`
	LastUpdated        time.Time `json:"last_updated"`
}

func Initialize() error {
	var err error

	if err = ensureDatabaseExists(); err != nil {
		return fmt.Errorf("failed to ensure database exists: %w", err)
	}

	connStr := fmt.Sprintf("host=%s port=%d user=%s password=%s dbname=%s sslmode=%s",
		config.GlobalAppConfig.DBhost,
		config.GlobalAppConfig.DBport,
		config.GlobalAppConfig.DBuser,
		config.GlobalAppConfig.DBpassword,
		config.GlobalAppConfig.DBname,
		config.GlobalAppConfig.DBsslmode,
	)

	db, err = sql.Open("postgres", connStr)
	if err != nil {
		return fmt.Errorf("failed to connect to db: %w", err)
	}

	if err = db.Ping(); err != nil {
		return fmt.Errorf("failed to ping db: %w", err)
	}

	db.SetMaxOpenConns(25)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(5 * time.Minute)

	log.Printf("Successfully connected to db '%s'", config.GlobalAppConfig.DBname)

	if err = createTables(); err != nil {
		return fmt.Errorf("failed to create tables: %w", err)
	}

	return nil
}

func ensureDatabaseExists() error {
	if err := validatePostgreSQLConnection(); err != nil {
		return fmt.Errorf("db connection validation failed: %w", err)
	}

	connStr := fmt.Sprintf("host=%s port=%d user=%s password=%s dbname=postgres sslmode=%s",
		config.GlobalAppConfig.DBhost,
		config.GlobalAppConfig.DBport,
		config.GlobalAppConfig.DBuser,
		config.GlobalAppConfig.DBpassword,
		config.GlobalAppConfig.DBsslmode,
	)

	defaultDB, err := sql.Open("postgres", connStr)
	if err != nil {
		return fmt.Errorf("failed to connect to default postgres db: %w", err)
	}
	defer defaultDB.Close()

	if err = defaultDB.Ping(); err != nil {
		return fmt.Errorf("failed to ping default postgres db: %w", err)
	}

	var exists bool
	checkQuery := `SELECT EXISTS(SELECT datname FROM pg_catalog.pg_database WHERE datname = $1)`

	err = defaultDB.QueryRow(checkQuery, config.GlobalAppConfig.DBname).Scan(&exists)
	if err != nil {
		return fmt.Errorf("failed to check if database exists: %w", err)
	}

	if !exists {
		log.Printf("db '%s' does not exist. Creating...", config.GlobalAppConfig.DBname)

		createQuery := fmt.Sprintf("CREATE DATABASE %s",
			quoteLiteral(config.GlobalAppConfig.DBname))

		_, err = defaultDB.Exec(createQuery)
		if err != nil {
			return fmt.Errorf("failed to create db '%s': %w", config.GlobalAppConfig.DBname, err)
		}

		log.Printf("Successfully created db '%s'", config.GlobalAppConfig.DBname)
	} else {
		log.Printf("Database '%s' already exists", config.GlobalAppConfig.DBname)
	}

	return nil
}

func validatePostgreSQLConnection() error {
	connStr := fmt.Sprintf("host=%s port=%d user=%s password=%s dbname=postgres sslmode=%s",
		config.GlobalAppConfig.DBhost,
		config.GlobalAppConfig.DBport,
		config.GlobalAppConfig.DBuser,
		config.GlobalAppConfig.DBpassword,
		config.GlobalAppConfig.DBsslmode,
	)

	testDB, err := sql.Open("postgres", connStr)
	if err != nil {
		return fmt.Errorf("failed to open connection to PostgreSQL server at %s:%d - check if PostgreSQL is running and accessible: %w",
			config.GlobalAppConfig.DBhost, config.GlobalAppConfig.DBport, err)
	}
	defer testDB.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err = testDB.PingContext(ctx); err != nil {
		return fmt.Errorf("failed to connect to PostgreSQL server at %s:%d with user '%s' - check credentials and permissions: %w",
			config.GlobalAppConfig.DBhost, config.GlobalAppConfig.DBport,
			config.GlobalAppConfig.DBuser, err)
	}

	log.Printf("Successfully validated PostgreSQL connection at %s:%d",
		config.GlobalAppConfig.DBhost, config.GlobalAppConfig.DBport)

	return nil
}

func quoteLiteral(name string) string {
	for _, char := range name {
		if !((char >= 'a' && char <= 'z') ||
			(char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') ||
			char == '_' || char == '-') {
			log.Fatalf("Invalid database name: %s", name)
		}
	}
	return fmt.Sprintf(`"%s"`, name)
}

func createTables() error {
	erc20TokensTable := `
	CREATE TABLE IF NOT EXISTS erc20_tokens (
		id BIGSERIAL PRIMARY KEY,
		address VARCHAR(42) UNIQUE NOT NULL,
		name VARCHAR(255),
		symbol VARCHAR(10),
		decimals SMALLINT,
		total_supply VARCHAR(78), -- Can handle very large numbers as string
		block_number BIGINT NOT NULL,
		block_hash VARCHAR(66),
		tx_hash VARCHAR(66),
		created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
		updated_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
	);`

	addressIndex := `
	CREATE INDEX IF NOT EXISTS idx_erc20_tokens_address ON erc20_tokens(address);`

	blockIndex := `
	CREATE INDEX IF NOT EXISTS idx_erc20_tokens_block_number ON erc20_tokens(block_number);`

	indexerStateTable := `
	CREATE TABLE IF NOT EXISTS indexer_state (
		id SERIAL PRIMARY KEY,
		last_processed_block BIGINT NOT NULL DEFAULT 0,
		total_contracts BIGINT NOT NULL DEFAULT 0,
		last_updated TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
	);`

	if _, err := db.Exec(erc20TokensTable); err != nil {
		return fmt.Errorf("failed to create erc20_tokens table: %w", err)
	}

	if _, err := db.Exec(addressIndex); err != nil {
		return fmt.Errorf("failed to create address index: %w", err)
	}

	if _, err := db.Exec(blockIndex); err != nil {
		return fmt.Errorf("failed to create block_number index: %w", err)
	}

	if _, err := db.Exec(indexerStateTable); err != nil {
		return fmt.Errorf("failed to create indexer_state table: %w", err)
	}

	log.Println("Database tables created/verified successfully")

	if err := initializeIndexerState(); err != nil {
		return fmt.Errorf("failed to initialize indexer state: %w", err)
	}

	return nil
}

func initializeIndexerState() error {
	var count int
	err := db.QueryRow("SELECT COUNT(*) FROM indexer_state").Scan(&count)
	if err != nil {
		return err
	}

	if count == 0 {
		_, err = db.Exec(`
			INSERT INTO indexer_state (last_processed_block, total_contracts, last_updated) 
			VALUES (0, 0, CURRENT_TIMESTAMP)
		`)
		if err != nil {
			return err
		}
		log.Println("Initialized indexer state in database")
	}

	return nil
}

func SaveERC20Token(token *ERC20Token) error {
	query := `
		INSERT INTO erc20_tokens (address, name, symbol, decimals, total_supply, block_number, block_hash, tx_hash)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (address) DO NOTHING
		RETURNING id, created_at, updated_at
	`

	err := db.QueryRow(query,
		token.Address,
		token.Name,
		token.Symbol,
		token.Decimals,
		token.TotalSupply,
		token.BlockNumber,
		token.BlockHash,
		token.TxHash,
	).Scan(&token.ID, &token.CreatedAt, &token.UpdatedAt)

	if err == sql.ErrNoRows {
		log.Printf("ERC20 token %s already exists in database", token.Address)
		return nil
	}

	if err != nil {
		return fmt.Errorf("failed to save ERC20 token: %w", err)
	}

	log.Printf("Saved ERC20 token: %s (%s) at block %d", token.Address, token.Symbol, token.BlockNumber)
	return nil
}

func GetERC20TokenByAddress(address string) (*ERC20Token, error) {
	token := &ERC20Token{}

	query := `
		SELECT id, address, name, symbol, decimals, total_supply, block_number, block_hash, tx_hash, created_at, updated_at
		FROM erc20_tokens WHERE address = $1
	`

	err := db.QueryRow(query, address).Scan(
		&token.ID,
		&token.Address,
		&token.Name,
		&token.Symbol,
		&token.Decimals,
		&token.TotalSupply,
		&token.BlockNumber,
		&token.BlockHash,
		&token.TxHash,
		&token.CreatedAt,
		&token.UpdatedAt,
	)

	if err == sql.ErrNoRows {
		return nil, nil
	}

	if err != nil {
		return nil, fmt.Errorf("failed to get ERC20 token: %w", err)
	}

	return token, nil
}

func GetAllERC20Tokens(offset, limit int) ([]ERC20Token, error) {
	query := `
		SELECT id, address, name, symbol, decimals, total_supply, block_number, block_hash, tx_hash, created_at, updated_at
		FROM erc20_tokens 
		ORDER BY created_at DESC 
		LIMIT $1 OFFSET $2
	`

	rows, err := db.Query(query, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("failed to query ERC20 tokens: %w", err)
	}
	defer rows.Close()

	var tokens []ERC20Token
	for rows.Next() {
		var token ERC20Token
		err := rows.Scan(
			&token.ID,
			&token.Address,
			&token.Name,
			&token.Symbol,
			&token.Decimals,
			&token.TotalSupply,
			&token.BlockNumber,
			&token.BlockHash,
			&token.TxHash,
			&token.CreatedAt,
			&token.UpdatedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan ERC20 token: %w", err)
		}
		tokens = append(tokens, token)
	}

	return tokens, nil
}

func SaveIndexerState(state *IndexerState) error {
	query := `
		UPDATE indexer_state 
		SET last_processed_block = $1, total_contracts = $2, last_updated = CURRENT_TIMESTAMP
		WHERE id = 1
	`

	_, err := db.Exec(query, state.LastProcessedBlock, state.TotalContracts)
	if err != nil {
		return fmt.Errorf("failed to save indexer state: %w", err)
	}

	return nil
}

func LoadIndexerState() (*IndexerState, error) {
	state := &IndexerState{}

	query := `
		SELECT id, last_processed_block, total_contracts, last_updated
		FROM indexer_state WHERE id = 1
	`

	err := db.QueryRow(query).Scan(
		&state.ID,
		&state.LastProcessedBlock,
		&state.TotalContracts,
		&state.LastUpdated,
	)

	if err == sql.ErrNoRows {
		return &IndexerState{
			ID:                 1,
			LastProcessedBlock: 0,
			TotalContracts:     0,
			LastUpdated:        time.Now(),
		}, nil
	}

	if err != nil {
		return nil, fmt.Errorf("failed to load indexer state: %w", err)
	}

	return state, nil
}

func GetTotalERC20Count() (uint64, error) {
	var count uint64
	err := db.QueryRow("SELECT COUNT(*) FROM erc20_tokens").Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("failed to get total ERC20 count: %w", err)
	}
	return count, nil
}

func TokenExists(address string) (bool, error) {
	var exists bool
	query := "SELECT EXISTS(SELECT 1 FROM erc20_tokens WHERE address = $1)"

	err := db.QueryRow(query, address).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("failed to check if token exists: %w", err)
	}

	return exists, nil
}

func Close() error {
	if db != nil {
		return db.Close()
	}
	return nil
}

func GetDB() *sql.DB {
	return db
}
