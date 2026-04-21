package state

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

// Store provides SQLite-backed persistence for tasks.
type Store struct {
	db *sql.DB
}

// DefaultDBPath returns the default database file path: ~/.vairdict/state.db
func DefaultDBPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolving home directory: %w", err)
	}
	return filepath.Join(home, ".vairdict", "state.db"), nil
}

// NewStore opens (or creates) a SQLite database at the given path and
// runs migrations. The caller must call Close when done.
func NewStore(dbPath string) (*Store, error) {
	dir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("creating db directory %s: %w", dir, err)
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("opening database %s: %w", dbPath, err)
	}

	// Enable WAL mode for better concurrent read performance.
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("setting WAL mode: %w", err)
	}

	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("running migrations: %w", err)
	}

	slog.Debug("state store opened", "path", dbPath)
	return s, nil
}

// Close closes the underlying database connection.
func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) migrate() error {
	schema := `
	CREATE TABLE IF NOT EXISTS tasks (
		id         TEXT PRIMARY KEY,
		intent     TEXT NOT NULL,
		state      TEXT NOT NULL,
		phase      TEXT NOT NULL DEFAULT '',
		loop_count TEXT NOT NULL DEFAULT '{}',
		assumptions TEXT NOT NULL DEFAULT '[]',
		attempts   TEXT NOT NULL DEFAULT '[]',
		depends_on TEXT NOT NULL DEFAULT '[]',
		priority   TEXT NOT NULL DEFAULT 'normal',
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	);
	CREATE INDEX IF NOT EXISTS idx_tasks_state ON tasks(state);
	`
	if _, err := s.db.Exec(schema); err != nil {
		return fmt.Errorf("creating tasks table: %w", err)
	}
	// Additive migrations for databases that predate later columns. SQLite
	// ALTER TABLE ADD COLUMN is idempotent via a duplicate-column error
	// check so re-running is safe.
	for _, alter := range []struct {
		column string
		stmt   string
	}{
		{"depends_on", `ALTER TABLE tasks ADD COLUMN depends_on TEXT NOT NULL DEFAULT '[]'`},
		{"priority", `ALTER TABLE tasks ADD COLUMN priority TEXT NOT NULL DEFAULT 'normal'`},
	} {
		if _, err := s.db.Exec(alter.stmt); err != nil && !isDuplicateColumnErr(err) {
			return fmt.Errorf("adding %s column: %w", alter.column, err)
		}
	}
	return nil
}

func isDuplicateColumnErr(err error) bool {
	return err != nil && (contains(err.Error(), "duplicate column") || contains(err.Error(), "already exists"))
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}

// CreateTask persists a new task. Returns an error if a task with the same ID exists.
func (s *Store) CreateTask(t *Task) error {
	loopJSON, err := json.Marshal(t.LoopCount)
	if err != nil {
		return fmt.Errorf("marshaling loop_count: %w", err)
	}
	assumptionsJSON, err := json.Marshal(t.Assumptions)
	if err != nil {
		return fmt.Errorf("marshaling assumptions: %w", err)
	}
	attemptsJSON, err := json.Marshal(t.Attempts)
	if err != nil {
		return fmt.Errorf("marshaling attempts: %w", err)
	}
	dependsOnJSON, err := json.Marshal(t.DependsOn)
	if err != nil {
		return fmt.Errorf("marshaling depends_on: %w", err)
	}

	priority := t.Priority
	if priority == "" {
		priority = "normal"
	}

	_, err = s.db.Exec(
		`INSERT INTO tasks (id, intent, state, phase, loop_count, assumptions, attempts, depends_on, priority, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		t.ID, t.Intent, string(t.State), string(t.Phase),
		string(loopJSON), string(assumptionsJSON), string(attemptsJSON), string(dependsOnJSON),
		priority,
		t.CreatedAt.Format(time.RFC3339Nano), t.UpdatedAt.Format(time.RFC3339Nano),
	)
	if err != nil {
		return fmt.Errorf("inserting task %s: %w", t.ID, err)
	}
	return nil
}

// GetTask retrieves a task by ID. Returns sql.ErrNoRows if not found.
func (s *Store) GetTask(id string) (*Task, error) {
	row := s.db.QueryRow(
		`SELECT id, intent, state, phase, loop_count, assumptions, attempts, depends_on, priority, created_at, updated_at
		 FROM tasks WHERE id = ?`, id,
	)
	return s.scanTask(row)
}

// UpdateTask persists changes to an existing task.
func (s *Store) UpdateTask(t *Task) error {
	loopJSON, err := json.Marshal(t.LoopCount)
	if err != nil {
		return fmt.Errorf("marshaling loop_count: %w", err)
	}
	assumptionsJSON, err := json.Marshal(t.Assumptions)
	if err != nil {
		return fmt.Errorf("marshaling assumptions: %w", err)
	}
	attemptsJSON, err := json.Marshal(t.Attempts)
	if err != nil {
		return fmt.Errorf("marshaling attempts: %w", err)
	}
	dependsOnJSON, err := json.Marshal(t.DependsOn)
	if err != nil {
		return fmt.Errorf("marshaling depends_on: %w", err)
	}

	priority := t.Priority
	if priority == "" {
		priority = "normal"
	}

	result, err := s.db.Exec(
		`UPDATE tasks SET intent=?, state=?, phase=?, loop_count=?, assumptions=?, attempts=?, depends_on=?, priority=?, updated_at=?
		 WHERE id=?`,
		t.Intent, string(t.State), string(t.Phase),
		string(loopJSON), string(assumptionsJSON), string(attemptsJSON), string(dependsOnJSON),
		priority,
		t.UpdatedAt.Format(time.RFC3339Nano), t.ID,
	)
	if err != nil {
		return fmt.Errorf("updating task %s: %w", t.ID, err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("checking rows affected: %w", err)
	}
	if rows == 0 {
		return fmt.Errorf("updating task %s: not found", t.ID)
	}
	return nil
}

// ListTasks returns all tasks, optionally filtered by state.
// Pass an empty string to list all tasks.
func (s *Store) ListTasks(filterState TaskState) ([]*Task, error) {
	var rows *sql.Rows
	var err error

	if filterState == "" {
		rows, err = s.db.Query(
			`SELECT id, intent, state, phase, loop_count, assumptions, attempts, depends_on, priority, created_at, updated_at
			 FROM tasks ORDER BY created_at ASC`,
		)
	} else {
		rows, err = s.db.Query(
			`SELECT id, intent, state, phase, loop_count, assumptions, attempts, depends_on, priority, created_at, updated_at
			 FROM tasks WHERE state = ? ORDER BY created_at ASC`,
			string(filterState),
		)
	}
	if err != nil {
		return nil, fmt.Errorf("querying tasks: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var tasks []*Task
	for rows.Next() {
		t, err := s.scanTaskRow(rows)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating tasks: %w", err)
	}
	return tasks, nil
}

// scanner is satisfied by both *sql.Row and *sql.Rows.
type scanner interface {
	Scan(dest ...any) error
}

func (s *Store) scanTask(row *sql.Row) (*Task, error) {
	return s.scanFromScanner(row)
}

func (s *Store) scanTaskRow(rows *sql.Rows) (*Task, error) {
	return s.scanFromScanner(rows)
}

func (s *Store) scanFromScanner(sc scanner) (*Task, error) {
	var (
		t               Task
		state, phase    string
		loopJSON        string
		assumptionsJSON string
		attemptsJSON    string
		dependsOnJSON   string
		priority        string
		createdAt       string
		updatedAt       string
	)

	err := sc.Scan(&t.ID, &t.Intent, &state, &phase,
		&loopJSON, &assumptionsJSON, &attemptsJSON, &dependsOnJSON, &priority,
		&createdAt, &updatedAt)
	if err != nil {
		return nil, fmt.Errorf("scanning task: %w", err)
	}
	t.Priority = priority

	t.State = TaskState(state)
	t.Phase = Phase(phase)

	t.LoopCount = make(map[Phase]int)
	if err := json.Unmarshal([]byte(loopJSON), &t.LoopCount); err != nil {
		return nil, fmt.Errorf("unmarshaling loop_count: %w", err)
	}

	if err := json.Unmarshal([]byte(assumptionsJSON), &t.Assumptions); err != nil {
		return nil, fmt.Errorf("unmarshaling assumptions: %w", err)
	}

	if err := json.Unmarshal([]byte(attemptsJSON), &t.Attempts); err != nil {
		return nil, fmt.Errorf("unmarshaling attempts: %w", err)
	}

	if dependsOnJSON != "" {
		if err := json.Unmarshal([]byte(dependsOnJSON), &t.DependsOn); err != nil {
			return nil, fmt.Errorf("unmarshaling depends_on: %w", err)
		}
	}

	t.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return nil, fmt.Errorf("parsing created_at: %w", err)
	}
	t.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedAt)
	if err != nil {
		return nil, fmt.Errorf("parsing updated_at: %w", err)
	}

	return &t, nil
}
