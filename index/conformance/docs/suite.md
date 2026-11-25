# The index suite

The cases of `index_suite`, in the order they run. Each one speaks the contract and nothing else, over a client of the kit for the write side and one for the read side. The driver is started under the kit's declaration (`index-conformance --print-declaration`): the kinds `kit-events` and `kit-certs`; the columns `action` (text, words, filter, required), `status_code`, `actor` and `trace` (text, filter), `status` (int, filter), `product` (text) and `recorded_ms` (int). The cases that need rows apply a fixture of five rows to the run's own scope once (the first case that needs it does it, so a case may also run alone): tenant `acme` has `rec-1`, `rec-2`, `rec-3` and `rec-5`, tenant `globex` has `rec-4`; the times are one second apart in that order; the actions are `token.exchange`, `token.refresh`, `key.sign`, `token.exchange`, `key.sign`.

| Case | What it holds a driver to |
| --- | --- |
| `cursor_of_a_log_never_indexed` | `GetCursor` of a log nobody wrote has size 0 and no root |
| `apply_writes_rows_and_moves_the_cursor` | `Apply` of five new rows with a cursor of size 5 answers 5 inserted and 0 duplicates, and `GetCursor` returns the size, the 32-byte root and the state that were written |
| `a_repeat_is_not_a_second_row` | `Apply` of the same rows again answers 0 inserted and 5 duplicates, and a query over every tenant still finds 5 |
| `the_cursor_moves_only_forward_by_condition` | a write at a cursor that is not the current one is `aborted`/`version`, field `expect_size`; a next cursor that does not grow is `invalid_argument`/`argument`, field `next.size`; a root of 31 bytes is `next.root`; none of them moves the cursor; a write at the current cursor does |
| `apply_refuses_what_it_cannot_hold` | a row with a hash of 8 bytes, with an empty entry, and with the tenant `*` are `invalid_argument`/`argument` with the field `rows.hash`, `rows.entry` and `rows.tenant`; a request with no log is `log`; a write to a kind that is not declared is `permission_denied`/`rule`, field `log.kind`; a refused write stores nothing |
| `search_names_its_partitions_and_tenants` | a query with no partitions, with a partition of a kind that is not declared, with no tenants, and with `*` beside a tenant is `invalid_argument`/`argument`, field `partitions` or `tenants` |
| `search_by_tenant_and_partition` | one tenant gets its rows only; two tenants get both tenants' rows; `*` gets every row of the partition; a tenant with no rows gets none; another scope's partition gets none; two partitions, one of them empty, get the rows of the first |
| `search_newest_first_in_pages` | by pages of the default size, 1, 2 and 3: every row once, newest first, never a page larger than asked, the token empty on the last page |
| `search_filters` | exact matches of the declared filter columns: `action`, `status_code`, `status` (an int), `actor`, `trace`, two matches that must both hold; `entry_id`; the time range (`from_ms` at or after, `to_ms` before); a match with a tenant |
| `search_by_words` | `exchange` finds the rows whose action has that word, `tok*` the rows with a word that starts so, `token exchange` needs both words, `token key` finds no row that has no `key`, a word that is in no row finds none, and a word of a column that is not a word column (`kit` of `product`, `actor` of `actor`) finds none |
| `the_declared_columns_are_held` | a row with an undeclared column, a text value in an int column, an int value in a text column, or the required `action` left out is `invalid_argument`/`argument`, field `rows.columns`; a match on a column that is not a filter column or not declared is `matches.column`, and one with a value of another type `matches.value`; a row without `trace` (not required) is stored and comes back without it |
| `a_row_comes_back_as_written` | every fixed column, every declared column and the entry come back as they were written, with the scope, the log kind and the log id of the log the row is in |

## What the suite fixes beyond the proto comments

These are the choices the contract left open. Every driver makes them, and a consumer may rely on them. They are in the crate documentation of `atoom_index_conformance` and, as prose, in the [index port](../../docs/index-port.md).

- Only the declared word columns are searchable, and the words of a value are its alphanumeric runs (`token.exchange` has the words `token` and `exchange`).
- A refusal names the field at fault by the paths listed above.
- A time range is `from_ms` at or after and `to_ms` before.

## What the suite cannot test

It cannot restart the driver, so that the index survives a restart is the driver's own test. It cannot change what a driver's engine does with words (the sqlite driver's tokenizer, `unicode61` with `remove_diacritics 2`, folds diacritics; the suite does not ask for it, since other engines may fold differently); that is the driver's backend suite. It cannot make the backend unavailable, so the `unavailable`/`engine` refusal is a driver test. It cannot remove what it wrote.
