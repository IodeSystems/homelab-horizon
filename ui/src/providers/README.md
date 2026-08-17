# providers/

WEB-8's fourth canonical folder, alongside `api/`, `components/` and `routes/`.

React context providers live here. hz's one provider — `SyncProvider` — predates
this folder and still sits in `components/`; it moves the next time it is edited
rather than in a rename commit that touches every import for no behavioural
reason.
