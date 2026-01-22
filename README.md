# Flashcards CLI (Go)

Simple terminal flashcards with a SQLite backend.

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
