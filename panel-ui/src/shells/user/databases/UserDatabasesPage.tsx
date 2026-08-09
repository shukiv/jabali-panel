// User shell counterpart to DatabasesPage: stacks the user's own
// databases and DB-users tables on a single route. Components are
// claim-aware server-side, so the admin list/create components work
// unchanged here for the list side; the user's create form stays
// scoped to its own username prefix.
import { UserDatabaseList } from "./UserDatabaseList";
import { UserDatabaseUsersList } from "../database-users/UserDatabaseUsersList";
import { RedisAccessCard } from "./RedisAccessCard";

export const UserDatabasesPage = () => (
  <div style={{ display: "flex", flexDirection: "column", gap: 24 }}>
    <UserDatabaseList />
    <UserDatabaseUsersList />
    <RedisAccessCard />
  </div>
);
