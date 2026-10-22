package chaos

import (
	"context"
	"net/http"
)

func contextWithoutCancel(r *http.Request) context.Context { return context.WithoutCancel(r.Context()) }
