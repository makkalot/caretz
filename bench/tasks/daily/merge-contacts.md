I have two contact exports, `phone_contacts.csv` and `work_contacts.csv`, with a lot of overlap. Please merge them into a single `contacts.csv`.

- Two rows are the same person when their email addresses match, ignoring upper/lower case and surrounding spaces.
- When a person appears in both files, keep the name, phone, and date from whichever row has the more recent `updated` date.
- Write emails in lowercase without surrounding spaces.
- Use the columns `name,email,phone,updated`, in that order, with a header row.
- Sort the rows by name, A to Z.

Don't modify the two input files.
