# Architecture Doc Best Practices

`architecture.md` describes standards for writing code in this repo. This doc describes guidelines for updating architecture.md.

`architecture.md` also describes a high level overview of the system for humans (and a reference guide for LLMs). 

## Document Re-usable Components
Any time you create a component that is meant to be re-usable, update architecture.md with a statement requiring future code authors use it. Be sure to describe what it does.

Claude Code often re-invents things that already exists. Noting the re-usable component in architecture.md guards against this.

## Keep high level overview up to date
Any time you change something which modifies the high level overview of the system in `architecture.md` you need to update the document to reflect the code changes you made.