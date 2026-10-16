

### E3 Task3 setup-expiry classification — coordinator

Source review D1 is mandatory and repaired, independent validation pending:

- Setup cancellation/deadline before BEGIN is a precommit ErrStoreBusy, preserving the underlying context error. Classification tests the returned error with errors.Is rather than ctx.Err(), so an unrelated I/O error coinciding with deadline expiry is not converted into busy. No new budget, worker, public signature, model or schema change.

Existing unchanged causal deadline RED/GREEN retained. No historical09cause claim.
