# Demo calculator

A deliberately tiny POSIX-shell project used by Ants tranche 1 to prove the
full pipeline: request -> plan/spec -> parallel isolated tasks -> integration
-> tests -> report.

Initially the repository cannot evaluate any operation: `lib_add.sh` and
`lib_mul.sh` do not exist, so `tests/calc_test.sh` fails. A completed run adds
both files on isolated branches, integrates them, and turns the suite green.

Try it manually:

    sh calc.sh add 2 3        # fails until lib_add.sh exists
    bash tests/calc_test.sh   # exits 1 until both features exist

The `.ants/capabilities.yaml` file is part of the fixture contract: it
declares which change kinds this project accepts so the deterministic planner
can produce an honest spec instead of inventing one.
