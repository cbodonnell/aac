# aac

Assets as code: building blocks for editors whose documents are Go source. A person edits
data in a tool, and the tool writes the change back into the `.go` file the data came from,
so an edit made in the tool and one made by hand are the same kind of diff.

- `edit`: undo history, splices of new source over old, whole-file writes.
- `golit`: a codec that reads a Go composite literal into a value by reflection, keeping
  how each part is spelled: named constants (read from their package's source), registered
  vars, calls of registered helpers, and anything else as opaque, its value the build's.
  Writing a changed value back as splices is planned.
