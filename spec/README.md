# Behavioral specification

Gherkin feature files that define the behavior of the `claude-sandbox` CLI.
They are the **spec**, not an executable integration suite: the Go
implementation's Ginkgo tests are the executable assertions, and each test
references the scenario it implements by ID.

## Conventions

- **Scenario IDs are stable.** Every scenario title starts with an ID like
  `CS-INIT-007`. Ginkgo `It` descriptions must include the ID verbatim, e.g.
  `It("CS-INIT-007: ...")`. `scripts/check-spec-coverage.sh` fails CI when a
  scenario ID has no matching test.
- **`@new`** marks behavior that did not exist in the bash implementation
  (added as part of the Go rewrite). Everything untagged is parity with the
  bash scripts as of the rewrite.
- **`@changed`** marks parity scenarios whose behavior was deliberately
  altered during the rewrite (the scenario text describes the NEW behavior;
  a comment describes what the bash version did).
- **`@manual`** marks scenarios verified by the manual smoke checklist rather
  than automated tests (real Docker / tty interplay).

## Files

| File | Area |
|---|---|
| `paths.feature` | foreign-path resolver, find-up/collect-up |
| `config-cascade.feature` | config deep-merge, env stacking, trackInHost cascade |
| `init.feature` | `init` subcommand incl. new prompt/flag behavior |
| `init-ralph.feature` | `init-ralph` scaffolding |
| `layout.feature` | `.claude-sandbox/` layout lifecycle (gitignore, sidecar) |
| `launch.feature` | mount assembly, injections, precedence, container command |
| `image-build.feature` | base/child image staleness, update check, version stamp |
| `ralph-loop.feature` | ralph iteration lifecycle, lock, stop, logs |
| `ralph-quota.feature` | outcome classification, quota/rate-limit handling |
