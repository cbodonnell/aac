# aac

Assets as code: building blocks for editors whose documents are Go source. A person edits
data in a tool, and the tool writes the change back into the `.go` file the data came from,
so an edit made in the tool and one made by hand are the same kind of diff.

- `edit`: undo history, splices of new source over old, whole-file writes.
- `golit` (planned): a codec that reads a Go composite literal into a value by reflection
  and writes a changed value back as splices, keeping comments, named constants, vars and
  helper calls.
