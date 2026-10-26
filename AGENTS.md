## Code Quality Restrictions

### When editing code

- Use red/green TDD when adding features or fixing issues.

- No over-engineering: no overly defensive design, no unnecessary implementation, and no features added without the maintainer's permission.

- No duplicate or unnecessary tests, but ensure at least one test passes before finishing a task.

- Minimize mocking; test under real conditions where viable, and ask the maintainer for keys or other information when needed.

- Think twice before creating or editing a Markdown file; avoid unnecessary text in both documents and the user interface.

- Always follow clean code principles.

### When exploring

- Be open-minded; feel free to explore different approaches.

### When reviewing code

- Do not adopt the author's mindset; keep asking whether each implementation is necessary.

- Do not hold back on the assumption that something "might be the maintainer's specific requirement"; point out anything you consider inappropriate.

### When asked for an implementation plan

- Think through the details and state the decisions you made in the final plan.

### When writing documents

- Be brief but informative: maximize information density while keeping the text readable and quick to understand.

### When asked to commit changes

- Split the edits into several smaller commits if necessary.