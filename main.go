package main

import (
	"bytes"
	"database/sql"
	"embed"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/formatters"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"
	tea "github.com/charmbracelet/bubbletea"
	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

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

func getDataDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".fcards")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", err
	}
	return dir, nil
}

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

	dataDir, err := getDataDir()
	if err != nil {
		fmt.Fprintln(os.Stderr, "failed to get data directory:", err)
		os.Exit(1)
	}

	db, err := openDB(filepath.Join(dataDir, "flashcards.db"))
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

	shuffleQuestions(questions)

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

	entries, err := migrationsFS.ReadDir("migrations")
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
		body, err := migrationsFS.ReadFile(filepath.Join("migrations", name))
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

func shuffleQuestions(questions []Question) {
	if len(questions) < 2 {
		return
	}
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	rng.Shuffle(len(questions), func(i, j int) {
		questions[i], questions[j] = questions[j], questions[i]
	})
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
	mode         int
	questions    []Question
	index        int
	showAnswers  bool
	scrollOffset int
	width        int
	height       int
	groups       []TypeGroup
	groupIndex   int
	groupQuery   string
	groupSearch  bool
	db           *sql.DB
	err          error
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
		if m.mode == modeCards && m.index < len(m.questions) {
			maxScroll := cardMaxScroll(m.questions[m.index], m.showAnswers, m.width, m.height)
			m.scrollOffset = clampScroll(m.scrollOffset, maxScroll)
		}
	case tea.KeyMsg:
		if m.mode == modeGroup && m.groupSearch {
			if msg.String() == "ctrl+c" || msg.String() == "q" {
				return m, tea.Quit
			}
			switch msg.Type {
			case tea.KeyEsc:
				m.groupSearch = false
			case tea.KeyEnter:
				m.groupSearch = false
			case tea.KeyBackspace, tea.KeyCtrlH:
				m.groupQuery = dropLastRune(m.groupQuery)
			case tea.KeyRunes:
				m.groupQuery += string(msg.Runes)
			}
			return m, nil
		}
		switch msg.String() {
		case "ctrl+c", "q":
			return m, tea.Quit
		case "up", "k", "K":
			if m.mode == modeCards && m.index < len(m.questions) {
				if m.scrollOffset > 0 {
					m.scrollOffset--
				}
			} else if m.mode == modeGroup && m.groupIndex > 0 {
				m.groupIndex--
			}
		case "down", "j", "J":
			if m.mode == modeCards && m.index < len(m.questions) {
				maxScroll := cardMaxScroll(m.questions[m.index], m.showAnswers, m.width, m.height)
				if m.scrollOffset < maxScroll {
					m.scrollOffset++
				}
			} else if m.mode == modeGroup {
				filtered := filterGroups(m.groups, m.groupQuery)
				if m.groupIndex < len(filtered)-1 {
					m.groupIndex++
				}
			}
		case "/":
			if m.mode == modeGroup {
				m.groupSearch = true
				m.groupQuery = ""
			}
		case "enter":
			if m.mode == modeGroup {
				filtered := filterGroups(m.groups, m.groupQuery)
				if m.groupIndex >= 0 && m.groupIndex < len(filtered) {
					selected := filtered[m.groupIndex].Type
					questions, err := loadQuestions(m.db, selected)
					if err != nil {
						m.err = err
						return m, nil
					}
					shuffleQuestions(questions)
					m.mode = modeCards
					m.questions = questions
					m.index = 0
					m.showAnswers = false
					m.scrollOffset = 0
				}
			} else if m.index < len(m.questions) {
				m.showAnswers = !m.showAnswers
				m.scrollOffset = 0
			}
		case "l", "L":
			if m.mode == modeCards && m.index < len(m.questions) {
				m.index++
				m.showAnswers = false
				m.scrollOffset = 0
			}
		case "h", "H":
			if m.mode == modeCards && m.index > 0 {
				m.index--
				m.showAnswers = false
				m.scrollOffset = 0
			}
		}
	}
	if m.mode == modeGroup {
		filtered := filterGroups(m.groups, m.groupQuery)
		if m.groupIndex >= len(filtered) {
			m.groupIndex = len(filtered) - 1
		}
		if m.groupIndex < 0 {
			m.groupIndex = 0
		}
	}
	return m, nil
}

func (m model) View() string {
	if m.err != nil {
		return padToHeight(fmt.Sprintf("Error: %v\nq to quit\n", m.err), m.height)
	}
	if m.mode == modeGroup {
		view := renderGroupList(m.groups, m.groupIndex, m.width, m.height, m.groupQuery, m.groupSearch) + "\n"
		return padToHeight(view, m.height)
	}
	if m.index >= len(m.questions) {
		return padToHeight(orange+"No more questions in this session."+reset+"\nq to quit\n", m.height)
	}

	width := cardWidth(m.width)

	q := m.questions[m.index]
	maxScroll := cardMaxScroll(q, m.showAnswers, m.width, m.height)
	m.scrollOffset = clampScroll(m.scrollOffset, maxScroll)
	view := renderCard(q, m.showAnswers, m.index+1, len(m.questions), width, m.height, m.scrollOffset) + "\n"
	return padToHeight(view, m.height)
}

func renderCard(q Question, showAnswers bool, pos, total, width, height, scrollOffset int) string {
	inner := width - 2

	line := func(text string) string {
		return orange + "|" + reset + " " + padRight(text, inner-2) + " " + orange + "|" + reset
	}

	contentLines := buildCardContentLines(q, showAnswers, inner-2)
	visibleLines := visibleContentLines(len(contentLines), height)
	maxScroll := max(0, len(contentLines)-visibleLines)
	scrollOffset = clampScroll(scrollOffset, maxScroll)
	start := scrollOffset
	end := start + visibleLines
	if end > len(contentLines) {
		end = len(contentLines)
	}

	controls := "Enter: flip  •  H/L: next card"
	if showAnswers {
		controls = "H/L: next card  •  Enter: flip"
	}
	if len(contentLines) > visibleLines {
		controls = "Up/Down: scroll  •  " + controls
	}

	builder := strings.Builder{}
	builder.WriteString(orange)
	builder.WriteString("+" + strings.Repeat("-", inner) + "+\n")
	builder.WriteString(line(fmt.Sprintf("fcards%*s", inner-8, fmt.Sprintf("%d/%d", pos, total))) + "\n")
	builder.WriteString(orange)
	builder.WriteString("+" + strings.Repeat("-", inner) + "+\n")
	builder.WriteString(reset)

	for _, lineText := range contentLines[start:end] {
		builder.WriteString(line(lineText) + "\n")
	}
	builder.WriteString(line(controls) + "\n")

	builder.WriteString(orange)
	builder.WriteString("+" + strings.Repeat("-", inner) + "+")
	builder.WriteString(reset)

	return builder.String()
}

func renderGroupList(groups []TypeGroup, selected, width, height int, query string, searching bool) string {
	_ = width
	builder := strings.Builder{}
	builder.WriteString(orange)
	builder.WriteString("fcards — group by type")
	builder.WriteString(reset)
	builder.WriteString("\n\n")

	filtered := filterGroups(groups, query)
	if searching || strings.TrimSpace(query) != "" {
		builder.WriteString("Search: " + query + "\n\n")
	}

	if len(filtered) == 0 {
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
	if end > len(filtered) {
		end = len(filtered)
	}

	for i := start; i < end; i++ {
		g := filtered[i]
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
	if searching {
		builder.WriteString("Type to search  •  Enter/Esc: done  •  q: quit")
	} else {
		builder.WriteString("J/K: move  •  Enter: open  •  /: search  •  q: quit")
	}
	return builder.String()
}

func filterGroups(groups []TypeGroup, query string) []TypeGroup {
	trimmed := strings.TrimSpace(query)
	if trimmed == "" {
		return groups
	}
	needle := strings.ToLower(trimmed)
	filtered := make([]TypeGroup, 0, len(groups))
	for _, g := range groups {
		if strings.Contains(strings.ToLower(g.Type), needle) {
			filtered = append(filtered, g)
		}
	}
	return filtered
}

func dropLastRune(text string) string {
	if text == "" {
		return text
	}
	runes := []rune(text)
	return string(runes[:len(runes)-1])
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
	var codeLang string
	var codeLines []string

	for _, raw := range lines {
		line := strings.TrimRight(raw, "\r")
		trimmed := strings.TrimSpace(line)

		// Check for opening fence with optional language
		if strings.HasPrefix(trimmed, fence) && !inCode {
			inCode = true
			codeLang = strings.TrimPrefix(trimmed, fence)
			codeLang = strings.TrimSpace(codeLang)
			codeLines = []string{}
			continue
		}

		// Check for closing fence
		if strings.HasPrefix(trimmed, fence) && inCode {
			inCode = false
			// Highlight the collected code block
			code := strings.Join(codeLines, "\n")
			code = expandTabs(code, 4)
			highlighted := highlightCode(code, codeLang)
			highlightedLines := strings.Split(strings.TrimSuffix(highlighted, "\n"), "\n")

			for _, hl := range highlightedLines {
				prefix := nextPrefix
				if !used {
					prefix = firstPrefix
					used = true
				}
				out = append(out, prefix+hl)
			}
			continue
		}

		if inCode {
			codeLines = append(codeLines, line)
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

func buildCardContentLines(q Question, showAnswers bool, width int) []string {
	lines := []string{"QUESTION"}
	lines = append(lines, wrapLines(q.Text, width)...)
	lines = append(lines, "")

	if showAnswers {
		lines = append(lines, "ANSWERS")
		if len(q.Answers) == 0 {
			lines = append(lines, "(no answers stored)")
		} else {
			for _, ans := range q.Answers {
				lines = append(lines, formatAnswerLines(ans, width)...)
			}
		}
		lines = append(lines, "")
	}

	return lines
}

func visibleContentLines(total, height int) int {
	if total == 0 {
		return 0
	}
	if height <= 0 {
		return total
	}
	visible := height - 5
	if visible < 1 {
		visible = 1
	}
	if visible > total {
		visible = total
	}
	return visible
}

func cardWidth(termWidth int) int {
	width := 64
	if termWidth > 0 {
		width = termWidth / 2
	}
	if width < 34 {
		width = 34
	}
	return width
}

func cardMaxScroll(q Question, showAnswers bool, termWidth, termHeight int) int {
	width := cardWidth(termWidth)
	inner := width - 2
	contentLines := buildCardContentLines(q, showAnswers, inner-2)
	visible := visibleContentLines(len(contentLines), termHeight)
	if visible == 0 || len(contentLines) <= visible {
		return 0
	}
	return len(contentLines) - visible
}

func clampScroll(offset, max int) int {
	if offset < 0 {
		return 0
	}
	if offset > max {
		return max
	}
	return offset
}

func padToHeight(view string, height int) string {
	if height <= 0 || view == "" {
		return view
	}
	viewLines := strings.Split(view, "\n")
	if len(viewLines) >= height {
		// Truncate to height and clear any extra content
		return strings.Join(viewLines[:height], "\n")
	}
	// Pad with empty lines to fill the screen
	for len(viewLines) < height {
		viewLines = append(viewLines, strings.Repeat(" ", 80))
	}
	return strings.Join(viewLines, "\n")
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func highlightCode(code, lang string) string {
	lexer := lexers.Get(lang)
	if lexer == nil {
		lexer = lexers.Analyse(code)
	}
	if lexer == nil {
		lexer = lexers.Fallback
	}
	lexer = chroma.Coalesce(lexer)

	style := styles.Get("monokai")
	if style == nil {
		style = styles.Fallback
	}

	formatter := formatters.Get("terminal256")
	if formatter == nil {
		formatter = formatters.Fallback
	}

	iterator, err := lexer.Tokenise(nil, code)
	if err != nil {
		return code
	}

	var buf bytes.Buffer
	if err := formatter.Format(&buf, style, iterator); err != nil {
		return code
	}

	return buf.String()
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
	visWidth := visualWidth(text)
	if visWidth >= width {
		return truncateToVisualWidth(text, width)
	}
	return text + strings.Repeat(" ", width-visWidth)
}

func visualWidth(text string) int {
	inEscape := false
	width := 0
	for _, r := range text {
		if r == '\033' {
			inEscape = true
			continue
		}
		if inEscape {
			if r == 'm' {
				inEscape = false
			}
			continue
		}
		width++
	}
	return width
}

func truncateToVisualWidth(text string, maxWidth int) string {
	inEscape := false
	width := 0
	var result strings.Builder
	for _, r := range text {
		if r == '\033' {
			inEscape = true
			result.WriteRune(r)
			continue
		}
		if inEscape {
			result.WriteRune(r)
			if r == 'm' {
				inEscape = false
			}
			continue
		}
		if width >= maxWidth {
			break
		}
		result.WriteRune(r)
		width++
	}
	return result.String()
}
