package main

import (
	"database/sql"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	_ "modernc.org/sqlite"
)

type Question struct {
	ID      int
	Text    string
	Answers []string
	Type    string
}

const (
	orange = "\033[38;5;208m"
	reset  = "\033[0m"
)

func main() {
	var typeFilter string
	flag.StringVar(&typeFilter, "type", "", "filter questions by type")
	flag.Parse()

	db, err := openDB("flashcards.db")
	if err != nil {
		fmt.Fprintln(os.Stderr, "failed to open db:", err)
		os.Exit(1)
	}
	defer db.Close()

	if err := ensureSchema(db); err != nil {
		fmt.Fprintln(os.Stderr, "failed to init schema:", err)
		os.Exit(1)
	}

	if err := seedIfEmpty(db); err != nil {
		fmt.Fprintln(os.Stderr, "failed to seed:", err)
		os.Exit(1)
	}

	questions, err := loadQuestions(db, typeFilter)
	if err != nil {
		fmt.Fprintln(os.Stderr, "failed to load questions:", err)
		os.Exit(1)
	}
	if len(questions) == 0 {
		fmt.Fprintln(os.Stderr, "no questions found in database")
		os.Exit(1)
	}

	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	rng.Shuffle(len(questions), func(i, j int) {
		questions[i], questions[j] = questions[j], questions[i]
	})

	if err := runUI(questions); err != nil {
		fmt.Fprintln(os.Stderr, "ui error:", err)
		os.Exit(1)
	}
}

func openDB(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		return nil, err
	}
	return db, nil
}

func ensureSchema(db *sql.DB) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS questions (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			text TEXT NOT NULL,
			type TEXT NOT NULL DEFAULT ''
		);`,
		`CREATE TABLE IF NOT EXISTS answers (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			question_id INTEGER NOT NULL,
			text TEXT NOT NULL,
			FOREIGN KEY(question_id) REFERENCES questions(id)
		);`,
	}
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			return err
		}
	}
	return ensureQuestionTypeColumn(db)
}

func ensureQuestionTypeColumn(db *sql.DB) error {
	rows, err := db.Query(`PRAGMA table_info(questions);`)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var cid int
		var name string
		var colType string
		var notNull int
		var dfltValue sql.NullString
		var pk int
		if err := rows.Scan(&cid, &name, &colType, &notNull, &dfltValue, &pk); err != nil {
			return err
		}
		if name == "type" {
			return rows.Err()
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}

	_, err = db.Exec(`ALTER TABLE questions ADD COLUMN type TEXT NOT NULL DEFAULT '';`)
	return err
}

func seedIfEmpty(db *sql.DB) error {
	var count int
	if err := db.QueryRow(`SELECT COUNT(1) FROM questions;`).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return nil
	}

	seed := []struct {
		q string
		a []string
		t string
	}{
		{
			q: "What is Go's concurrency model built on?",
			a: []string{"Goroutines", "Channels"},
			t: "general",
		},
		{
			q: "Which SQL clause filters rows?",
			a: []string{"WHERE"},
			t: "general",
		},
		{
			q: "Name a Git command to list branches.",
			a: []string{"git branch"},
			t: "general",
		},
	}

	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() {
		_ = tx.Rollback()
	}()

	for _, card := range seed {
		res, err := tx.Exec(`INSERT INTO questions(text, type) VALUES (?, ?);`, card.q, card.t)
		if err != nil {
			return err
		}
		id, err := res.LastInsertId()
		if err != nil {
			return err
		}
		for _, ans := range card.a {
			if _, err := tx.Exec(`INSERT INTO answers(question_id, text) VALUES (?, ?);`, id, ans); err != nil {
				return err
			}
		}
	}

	return tx.Commit()
}

func loadQuestions(db *sql.DB, typeFilter string) ([]Question, error) {
	baseQuery := `
		SELECT q.id, q.text, q.type, a.text
		FROM questions q
		LEFT JOIN answers a ON q.id = a.question_id
	`
	var rows *sql.Rows
	var err error
	if strings.TrimSpace(typeFilter) != "" {
		rows, err = db.Query(baseQuery+` WHERE q.type = ? ORDER BY q.id, a.id;`, typeFilter)
	} else {
		rows, err = db.Query(baseQuery + ` ORDER BY q.id, a.id;`)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	byID := make(map[int]*Question)
	var order []int
	for rows.Next() {
		var id int
		var qText string
		var qType string
		var aText sql.NullString
		if err := rows.Scan(&id, &qText, &qType, &aText); err != nil {
			return nil, err
		}
		entry, ok := byID[id]
		if !ok {
			entry = &Question{ID: id, Text: qText, Type: qType}
			byID[id] = entry
			order = append(order, id)
		}
		if aText.Valid {
			entry.Answers = append(entry.Answers, aText.String)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	questions := make([]Question, 0, len(order))
	for _, id := range order {
		questions = append(questions, *byID[id])
	}
	return questions, nil
}

func runUI(questions []Question) error {
	p := tea.NewProgram(newModel(questions), tea.WithAltScreen())
	_, err := p.Run()
	return err
}

type model struct {
	questions   []Question
	index       int
	showAnswers bool
	width       int
	height      int
}

func newModel(questions []Question) model {
	return model{
		questions: questions,
		width:     64,
	}
}

func (m model) Init() tea.Cmd {
	return nil
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "q":
			return m, tea.Quit
		case "enter":
			if m.index < len(m.questions) {
				m.showAnswers = true
			}
		case "h", "l":
			if m.index < len(m.questions) {
				m.index++
				m.showAnswers = false
			}
		}
	}
	return m, nil
}

func (m model) View() string {
	if m.index >= len(m.questions) {
		return orange + "No more questions in this session." + reset + "\nq to quit\n"
	}

	width := 64
	if m.width > 0 {
		width = min(m.width-4, 72)
	}
	if width < 34 {
		width = 34
	}

	q := m.questions[m.index]
	return renderCard(q, m.showAnswers, m.index+1, len(m.questions), width) + "\n"
}

func renderCard(q Question, showAnswers bool, pos, total, width int) string {
	inner := width - 2

	line := func(text string) string {
		return "| " + padRight(text, inner-2) + " |"
	}

	builder := strings.Builder{}
	builder.WriteString(orange)
	builder.WriteString("+" + strings.Repeat("-", inner) + "+\n")
	builder.WriteString(line(fmt.Sprintf("fcards%*s", inner-8, fmt.Sprintf("%d/%d", pos, total))) + "\n")
	builder.WriteString("+" + strings.Repeat("-", inner) + "+\n")
	builder.WriteString(reset)

	builder.WriteString(line("QUESTION") + "\n")
	for _, lineText := range wrapLines(q.Text, inner-2) {
		builder.WriteString(line(lineText) + "\n")
	}
	builder.WriteString(line("") + "\n")

	if showAnswers {
		builder.WriteString(line("ANSWERS") + "\n")
		if len(q.Answers) == 0 {
			builder.WriteString(line("(no answers stored)") + "\n")
		} else {
			for _, ans := range q.Answers {
				for _, lineText := range wrapLines("- "+ans, inner-2) {
					builder.WriteString(line(lineText) + "\n")
				}
			}
		}
		builder.WriteString(line("") + "\n")
		builder.WriteString(line("H/L: next card  •  Enter: flip") + "\n")
	} else {
		builder.WriteString(line("Enter: flip  •  H/L: next card") + "\n")
	}

	builder.WriteString(orange)
	builder.WriteString("+" + strings.Repeat("-", inner) + "+")
	builder.WriteString(reset)

	return builder.String()
}

func wrapLines(text string, width int) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return []string{""}
	}

	words := strings.Fields(text)
	lines := []string{}
	var current strings.Builder

	for _, word := range words {
		if current.Len() == 0 {
			current.WriteString(word)
			continue
		}
		if current.Len()+1+len(word) > width {
			lines = append(lines, current.String())
			current.Reset()
			current.WriteString(word)
			continue
		}
		current.WriteString(" ")
		current.WriteString(word)
	}
	if current.Len() > 0 {
		lines = append(lines, current.String())
	}
	if len(lines) == 0 {
		lines = append(lines, "")
	}
	return lines
}

func padRight(text string, width int) string {
	runes := []rune(text)
	if len(runes) >= width {
		return string(runes[:width])
	}
	return text + strings.Repeat(" ", width-len(runes))
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
