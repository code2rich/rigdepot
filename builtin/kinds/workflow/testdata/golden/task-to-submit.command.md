description: 任务开发 → 单元测试 → 提测前自查 — 接到一个开发任务，要一直做到提测时 (workflow 从任务到提测, 3 steps)
---
# 从任务到提测

任务开发 → 单元测试 → 提测前自查

The workflow has 3 steps. You do not get them all at once: the command line hands them out one at a time, and only once the checks of the step before have passed.

1. In the project's directory, run:

   ```sh
   acme flow next task-to-submit
   ```

   It prints the step: which skill or agent does it, what else it may use, what must not be done, and the checks at its end.
2. Do that step, and only that step.
3. Run `acme flow next task-to-submit` again. It runs the checks that are commands itself. For a check you make, add `--evidence "<what shows it holds>"`. For a check the person makes, or when the step says to wait for the person, ask them and add `--confirmed` only after they have said yes in this conversation.
4. When it says a check did not pass, put it right and run it again. Never go on to the next step without it.

When it says the workflow is done, tell the person. To start again from the first step, run `acme flow next task-to-submit --restart`.

What the person gave you for this workflow: $ARGUMENTS
