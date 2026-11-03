package api

// Wiring (done by cmd/frozen-throne):
//
//	srv, err := api.New(api.Config{SessionSecret: secret, SecureCookies: true}, api.Deps{
//		Store:       st,
//		Engine:      engine.New(),
//		OAuth:       &api.GitHubOAuth{ClientID: id, ClientSecret: sec, RedirectURL: base + "/api/v1/auth/callback"},
//		Permissions: &api.GitHubPermissions{},
//		Notifier:    reconciler, // implements api.ChangeNotifier
//	})
//	http.ListenAndServe(addr, srv.Handler())
//
// Not implemented yet (answer 501): Slack settings endpoints, which depend on
// the notifications task.
