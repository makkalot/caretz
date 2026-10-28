I want a small bash script to back up a folder. Please write `backup.sh` so that

```text
./backup.sh SRC_DIR DEST_DIR
```

works like this:

- It creates `DEST_DIR/backup-YYYY-MM-DD.tar.gz` containing everything inside `SRC_DIR`, including subfolders.
- Paths inside the archive are relative to `SRC_DIR`, for example `notes/todo.txt`, not absolute paths and not prefixed with the folder name.
- Files ending in `.tmp` are left out.
- The date is today's date, unless the `BACKUP_DATE` environment variable is set, in which case its value is used as the date.
- It creates `DEST_DIR` if it doesn't exist.
- On success it prints the path of the archive it created.
- If it gets the wrong number of arguments, or `SRC_DIR` isn't a directory, it prints a usage message to stderr and exits with a non-zero status without creating anything.

Make the script executable. You can try it on `sample-data/`.
