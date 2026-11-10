package main

// unscoped runs fn with the transaction's row scope widened to '*' and restores the person's scope after it:
// the coordinator's own host and account choice inside a person's request sees every row,
// and filters in code (people-access §2).
func unscoped[T any](q *Request, fn func() (T, error)) (T, error) {
	var zero T
	if q.tx == nil || q.person == nil || q.person.Owner {
		return fn()
	}
	var ids, person string
	if err := q.tx.QueryRow(q.ctx(), "SELECT coalesce(current_setting('fleet.project_ids',true),''),coalesce(current_setting('fleet.person',true),'')").Scan(&ids, &person); err != nil {
		return zero, err
	}
	if _, err := q.tx.Exec(q.ctx(), "SELECT set_config('fleet.project_ids','*',true),set_config('fleet.person','',true)"); err != nil {
		return zero, err
	}
	out, err := fn()
	if _, rerr := q.tx.Exec(q.ctx(), "SELECT set_config('fleet.project_ids',$1,true),set_config('fleet.person',$2,true)", ids, person); rerr != nil && err == nil {
		err = rerr
	}
	return out, err
}
