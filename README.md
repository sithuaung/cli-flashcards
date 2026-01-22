# Flashcards CLI (Go)

Simple terminal flashcards with a SQLite backend.
<img width="1173" height="417" alt="Screenshot 2569-01-22 at 9 21 50 PM" src="https://github.com/user-attachments/assets/885624bd-6b37-48e4-a8eb-961a65278c3a" />

## Requirements
- Go 1.20+

## Getting started
```bash
go build -o fcards
./fcards
```

The app stores data in `flashcards.db` in the project directory.

## Usage
```bash
./fcards
./fcards -type general
./fcards -group type
```

Questions are randomly loaded. Can go to next/prev questions by pressing "h" or "l" just like vim.
To see the answer, press "enter". To quite, press "q"


Flags:
- `-type`: filter questions by type
- `-group`: group questions (currently supports `type`)

## Database + migrations
On startup the app runs SQL migrations found in `migrations/` and records
applied versions in the `schema_migrations` table.

To add a migration:
1. Create a new `migrations/NNN_description.sql` file.
2. Keep filenames ordered so they apply in sequence.

## Seeding
If the database is empty, a small sample set of questions is inserted
automatically.

## Import Q&A by yourself
