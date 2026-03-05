package storage

import (
	"context"
	"database/sql"
	"time"
)

// AgentRecord represents stored agent data
type AgentRecord struct {
	Hostname     string
	PublicKeyPEM string
	TokenJTI     string
	RegisteredAt  time.Time
}

// Store provides database access
type Store struct {
	db *sql.DB
}

// NewStore creates new database store
func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

// GetAgent retrieves agent by hostname
func (s *Store) GetAgent(ctx context.Context, hostname string) (*AgentRecord, error) {
	// TODO: Implement agent lookup
	return nil, nil
}

// UpsertAgent creates or updates agent
func (s *Store) UpsertAgent(ctx context.Context, hostname, publicKeyPEM, tokenJTI string) error {
	// TODO: Implement upsert
	return nil
}

// TODO: Add authorized_keys, blacklist methods
