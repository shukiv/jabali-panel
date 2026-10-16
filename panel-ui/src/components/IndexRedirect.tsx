// IndexRedirect — the element of an index route that only forwards to a
// child (a shell root to its dashboard).
//
// A plain <Navigate> fires once, when it mounts. Signing in navigates to the
// shell root twice (LoginPage's navigate() and PublicOnly's <Navigate>), and
// when the second lands while the index redirect's own navigation is still
// pending (the dashboard chunk loading), the router settles on the shell root
// with that <Navigate> still mounted and the same target: it never fires
// again and the page stays blank under the header until a reload. Keying it
// on the location makes every arrival at the index route redirect.
import { Navigate, useLocation } from "react-router";

export function IndexRedirect({ to }: { to: string }) {
  const { key } = useLocation();
  return <Navigate key={key} to={to} replace />;
}
