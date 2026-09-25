# Review agent calibration

The review agent (`flywheel review <task> --agent`, issue #389) is only useful if it finds the
defects that matter. This directory holds the defects an external reviewer did find, so review
quality becomes a number.

## The cases

`external-review-bugs.json` holds 142 cases from 76 past pull requests of this repository
(#232–#358). An external PR reviewer flagged each one as a bug. Each case has these fields:

| field | meaning |
|---|---|
| `pr` | the pull request number |
| `commit` | the PR head the external reviewer saw |
| `path` | the file, forward slashes, repo-relative |
| `line` | the line the reviewer commented on |
| `claim` | the first line of the reviewer's comment |

The cases are grouped by (`pr`, `commit`): one group is one PR state and gets one reviewer run.

## Running it

```sh
flywheel review calibrate --cases docs/calibration/external-review-bugs.json --session calibrator \
  [--sample 10] [--seed 1] [--worker NAME] [--window 15] [--main origin/main] [--out FILE]
```

For each sampled PR state:

1. The command checks that the commit exists (`git cat-file -e`). When it is missing, it runs
   `git fetch origin pull/<pr>/head`. If the commit is still missing, the group is skipped and the
   report says why.
2. The base is `git merge-base <commit> origin/main` (`--main` picks another ref).
3. `git worktree add --detach` checks the commit out in a temporary directory. The worktree is
   always removed afterwards (`git worktree remove --force`).
4. A synthetic ledger is built in the same temporary directory, never in this repository's
   ledger. It holds a copy of `.flywheel/config.json` (for the workers), a brief that owns the
   files changed between base and commit and has no gates, and `planned` and `dispatched` events
   whose `base` is the merge-base and whose `workdir` is the worktree.
5. The review agent runs over that unit exactly as `flywheel review --agent` would. Its findings
   are matched against the group's cases.

The report is printed and written to `--out`. The default is
`.flywheel/reviews/calibration-<UTC date>.md`. The command exits 0 when it writes a report.

The sample is deterministic: the same `--sample` and `--seed` always pick the same PR states, so
two runs with a changed prompt can be compared.

## Matching and recall

A finding **matches** a case when:

- the file paths are equal (forward slashes), and
- the lines are at most `--window` lines apart (default 15).

Each case is matched at most once, by the nearest finding. A finding that matches no case is an
**extra** finding. An extra finding is not necessarily wrong: the external reviewer did not flag
everything either.

- **recall** of a group = cases matched / cases in the group.
- **overall recall** = the sum of the matched cases / the sum of the cases, over the groups that
  were reviewed. Skipped groups do not count.

The report has a table per PR (cases, hits, recall, findings, extra, or the reason for a skip),
the totals, and the **missed claims**: the defects the agent did not find.

## Cost

Each sampled PR state is one reviewer run on the configured model: the staffing `reviewer` role,
else the default worker, or `--worker`. A refused answer costs a second run. `--sample 10` means
about ten model runs. Pick the sample size with that in mind, and never run a calibration in a
test.

## Improving the reviewer

The missed claims list is the input for improving the reviewer. Read each missed defect, work out
which class of defect the agent does not look for, and change `internal/flywheel/review_prompt.md`
and the reviewer skill. Then re-run with the same `--sample` and `--seed`, and compare the recall.
