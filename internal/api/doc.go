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
// Slack: pass Deps.Slack (a *slack.Notifier) to deliver notifications and
// the settings test endpoint; Config.DashboardURL sets the link base.
