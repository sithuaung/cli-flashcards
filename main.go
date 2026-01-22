package main

import (
	"database/sql"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
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

type TypeGroup struct {
	Type  string
	Count int
}

const (
	orange = "\033[38;5;208m"
	reset  = "\033[0m"
)

const (
	modeCards = iota
	modeGroup
)

func main() {
	var typeFilter string
	var groupBy string
	flag.StringVar(&typeFilter, "type", "", "filter questions by type")
	flag.StringVar(&groupBy, "group", "", "group questions (supported: type)")
	flag.Parse()

	db, err := openDB("flashcards.db") // sqlite
	if err != nil {
		fmt.Fprintln(os.Stderr, "failed to open db:", err)
		os.Exit(1)
	}
	defer db.Close()

	if err := runMigrations(db); err != nil {
		fmt.Fprintln(os.Stderr, "failed to init schema:", err)
		os.Exit(1)
	}

	if err := seedIfEmpty(db); err != nil {
		fmt.Fprintln(os.Stderr, "failed to seed:", err)
		os.Exit(1)
	}

	if strings.TrimSpace(groupBy) != "" {
		switch strings.ToLower(strings.TrimSpace(groupBy)) {
		case "type":
			groups, err := loadTypeGroups(db)
			if err != nil {
				fmt.Fprintln(os.Stderr, "failed to list questions by type:", err)
				os.Exit(1)
			}
			if err := runUI(newGroupModel(groups, db)); err != nil {
				fmt.Fprintln(os.Stderr, "ui error:", err)
				os.Exit(1)
			}
			return
		default:
			fmt.Fprintln(os.Stderr, "unsupported group:", groupBy)
			os.Exit(1)
		}
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

	if err := runUI(newCardsModel(questions)); err != nil {
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

func runMigrations(db *sql.DB) error {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		version TEXT PRIMARY KEY,
		applied_at TEXT NOT NULL
	);`); err != nil {
		return err
	}

	entries, err := os.ReadDir("migrations")
	if err != nil {
		return err
	}

	files := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.HasSuffix(name, ".sql") {
			files = append(files, name)
		}
	}
	sort.Strings(files)

	applied := make(map[string]bool)
	rows, err := db.Query(`SELECT version FROM schema_migrations;`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var version string
		if err := rows.Scan(&version); err != nil {
			return err
		}
		applied[version] = true
	}
	if err := rows.Err(); err != nil {
		return err
	}

	for _, name := range files {
		if applied[name] {
			continue
		}
		path := filepath.Join("migrations", name)
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.TrimSpace(string(body)) == "" {
			continue
		}

		tx, err := db.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(string(body)); err != nil {
			_ = tx.Rollback()
			return err
		}
		if _, err := tx.Exec(
			`INSERT INTO schema_migrations(version, applied_at) VALUES (?, ?);`,
			name,
			time.Now().UTC().Format(time.RFC3339),
		); err != nil {
			_ = tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
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

func loadTypeGroups(db *sql.DB) ([]TypeGroup, error) {
	rows, err := db.Query(`
		SELECT q.type, COUNT(1)
		FROM questions q
		GROUP BY q.type
		ORDER BY q.type;
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var groups []TypeGroup
	for rows.Next() {
		var qType string
		var count int
		if err := rows.Scan(&qType, &count); err != nil {
			return nil, err
		}
		groups = append(groups, TypeGroup{Type: qType, Count: count})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return groups, nil
}

func runUI(m tea.Model) error {
	p := tea.NewProgram(m, tea.WithAltScreen())
	_, err := p.Run()
	return err
}

type model struct {
	mode        int
	questions   []Question
	index       int
	showAnswers bool
	width       int
	height      int
	groups      []TypeGroup
	groupIndex  int
	db          *sql.DB
	err         error
}

func newCardsModel(questions []Question) model {
	return model{
		mode:      modeCards,
		questions: questions,
		width:     64,
	}
}

func newGroupModel(groups []TypeGroup, db *sql.DB) model {
	return model{
		mode:   modeGroup,
		groups: groups,
		width:  64,
		db:     db,
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
		case "j", "J":
			if m.mode == modeGroup && m.groupIndex < len(m.groups)-1 {
				m.groupIndex++
			}
		case "k", "K":
			if m.mode == modeGroup && m.groupIndex > 0 {
				m.groupIndex--
			}
		case "enter":
			if m.mode == modeGroup {
				if m.groupIndex >= 0 && m.groupIndex < len(m.groups) {
					selected := m.groups[m.groupIndex].Type
					questions, err := loadQuestions(m.db, selected)
					if err != nil {
						m.err = err
						return m, nil
					}
					m.mode = modeCards
					m.questions = questions
					m.index = 0
					m.showAnswers = false
				}
			} else if m.index < len(m.questions) {
				m.showAnswers = true
			}
		case "h", "l":
			if m.mode == modeCards && m.index < len(m.questions) {
				m.index++
				m.showAnswers = false
			}
		}
	}
	return m, nil
}

func (m model) View() string {
	if m.err != nil {
		return fmt.Sprintf("Error: %v\nq to quit\n", m.err)
	}
	if m.mode == modeGroup {
		return renderGroupList(m.groups, m.groupIndex, m.width, m.height) + "\n"
	}
	if m.index >= len(m.questions) {
		return orange + "No more questions in this session." + reset + "\nq to quit\n"
	}

	width := 64
	if m.width > 0 {
		width = m.width / 2
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
				for _, lineText := range formatAnswerLines(ans, inner-2) {
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

func renderGroupList(groups []TypeGroup, selected, width, height int) string {
	_ = width
	builder := strings.Builder{}
	builder.WriteString(orange)
	builder.WriteString("fcards — group by type")
	builder.WriteString(reset)
	builder.WriteString("\n\n")

	if len(groups) == 0 {
		builder.WriteString("No types found.\n")
		builder.WriteString("q to quit\n")
		return builder.String()
	}

	maxLines := height - 4
	if maxLines < 6 {
		maxLines = 6
	}
	start := 0
	if selected >= maxLines {
		start = selected - maxLines + 1
	}
	end := start + maxLines
	if end > len(groups) {
		end = len(groups)
	}

	for i := start; i < end; i++ {
		g := groups[i]
		name := strings.TrimSpace(g.Type)
		if name == "" {
			name = "(none)"
		}
		line := fmt.Sprintf("%s - %d", name, g.Count)
		if i == selected {
			builder.WriteString(orange)
			builder.WriteString("> " + line)
			builder.WriteString(reset)
		} else {
			builder.WriteString("  " + line)
		}
		builder.WriteString("\n")
	}
	builder.WriteString("\n")
	builder.WriteString("J/K: move  •  Enter: open  •  q: quit")
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

func formatAnswerLines(answer string, width int) []string {
	const firstPrefix = "- "
	const nextPrefix = "  "
	const fence = "```"

	lines := strings.Split(answer, "\n")
	out := []string{}
	inCode := false
	used := false

	for _, raw := range lines {
		line := strings.TrimRight(raw, "\r")
		if strings.HasPrefix(strings.TrimSpace(line), fence) {
			inCode = !inCode
			continue
		}

		if inCode {
			line = expandTabs(line, 4)
			prefix := nextPrefix
			if !used {
				prefix = firstPrefix
				used = true
			}
			out = append(out, prefix+line)
			continue
		}

		if strings.TrimSpace(line) == "" {
			if used {
				out = append(out, "")
			}
			continue
		}

		prefix := nextPrefix
		if !used {
			prefix = firstPrefix
		}
		space := width - len(prefix)
		if space < 1 {
			space = 1
		}
		wrapped := wrapLines(line, space)
		for i, w := range wrapped {
			p := nextPrefix
			if !used && i == 0 {
				p = firstPrefix
				used = true
			}
			out = append(out, p+w)
		}
	}

	return out
}

func expandTabs(s string, tabWidth int) string {
	if tabWidth <= 0 || !strings.Contains(s, "\t") {
		return s
	}
	var b strings.Builder
	col := 0
	for _, r := range s {
		if r == '\t' {
			spaceCount := tabWidth - (col % tabWidth)
			if spaceCount == 0 {
				spaceCount = tabWidth
			}
			b.WriteString(strings.Repeat(" ", spaceCount))
			col += spaceCount
			continue
		}
		b.WriteRune(r)
		col++
	}
	return b.String()
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
