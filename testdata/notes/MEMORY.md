# Team memory index

## Preferences
- **[Release notes](user_release_notes.md)**: one changelog line per change, newest first, the version in bold (all repos)
- [Snapshot before migrating](feedback_snapshot_first.md): snapshot the database first; restore it on staging before the real run
- Reviews: [two approvals](feedback_two_approvals.md) (main only); [no Friday deploys](feedback_no_friday_deploys.md)
- **Tag releases from main only**

## Projects
- [beacon](beacon.md): **v2.3.1 prod; v2.4.0 tagged + smoke PASS 2026-10-29, deploy awaits Mark** (release #24 incl. timeline #18); load test 3 findings open; open #17, #19, #21
- [ledger](ledger.md): **v1.8.0 live since 2026-10-25**; **main `a1b2c3d` has the VAT fix, CI red; rounding rule (#31) awaits Mark**; open: Mark's sign-off on the CSV import (#33)
- [harbour](harbour.md): **v3.1.0 released, prod on 3.1.0**; **arm64 build (#7) merged on main `9f8e7d6`, mirrored to GitHub; v3.2.0 release + deploy await Mark**; **GitHub #9 reply awaits Mark**
- harbour: [garbage collection notes](harbour_gc.md); known: untagged layers are skipped
- [office_network](office_network.md): **two access point models shortlisted 2026-10-30** at the office (rack B), guest VLAN done; which model to order awaits Mark
