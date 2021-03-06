package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"github.com/GuiaBolso/darwin"
	"github.com/Masterminds/squirrel"
	"github.com/coopernurse/maelstrom/pkg/common"
	"github.com/coopernurse/maelstrom/pkg/v1"
	"github.com/mattn/go-sqlite3"
	"github.com/mgutz/logxi/v1"
	"github.com/pkg/errors"
	"math/rand"
	"strconv"
	"strings"
	"time"
)

func NewSqlDb(driver string, dsn string) (*SqlDb, error) {
	db, err := sql.Open(driver, dsn)
	if err != nil {
		return nil, err
	}

	onConflictSql := "ON CONFLICT <TARGET> DO UPDATE SET "
	blobType := "mediumblob"
	if strings.Contains(driver, "postgres") {
		squirrel.StatementBuilder = squirrel.StatementBuilder.PlaceholderFormat(squirrel.Dollar)
		blobType = "jsonb"
	} else if strings.Contains(driver, "mysql") {
		onConflictSql = "ON DUPLICATE KEY UPDATE "
	}

	return &SqlDb{db: db, driver: driver, blobType: blobType, onConflictSql: onConflictSql}, nil
}

func NewTestSqlDb() *SqlDb {
	sqlDb, err := NewSqlDb("sqlite3", "file:test.db?cache=shared&_journal_mode=WAL&mode=memory&_busy_timeout=5000")
	//sqlDb, err := NewSqlDb("mysql", "root:test@(127.0.0.1:3306)/maeltest")
	//sqlDb, err := NewSqlDb("postgres", "postgres://postgres:test@127.0.0.1:5432/maeltest?sslmode=disable")
	panicOnErr(err)

	// run sql migrations and delete any existing data
	panicOnErr(sqlDb.Migrate())
	panicOnErr(sqlDb.DeleteAll())

	return sqlDb
}

type SqlDb struct {
	db            *sql.DB
	driver        string
	blobType      string
	onConflictSql string
	DebugLog      bool
}

func (d *SqlDb) Close() {
	err := d.db.Close()
	if err != nil {
		log.Error("sql_db: err closing db", "err", err)
	}
}

func (d *SqlDb) DeleteAll() error {
	tables := []string{"component", "eventsource", "nodestatus", "rolelock", "componentstart"}
	for _, t := range tables {
		_, err := d.db.Exec(fmt.Sprintf("delete from %s", t))
		if err != nil {
			return fmt.Errorf("sql_db: unable to delete from: %s - %v", t, err)
		}
	}
	return nil
}

func (d *SqlDb) AcquireOrRenewRole(roleId string, nodeId string, lockDur time.Duration) (bool, string, error) {
	for i := 0; i < 5; i++ {
		acquired, lockNodeId, err := d.acquireOrRenewRoleOnce(roleId, nodeId, lockDur)
		if lockNodeId == "" && err == nil {
			// retry terminal case where insert failed. this ensures we resolve the current nodeId
			time.Sleep(time.Duration(rand.Intn(50)+1) * time.Millisecond)
		} else {
			return acquired, lockNodeId, err
		}
	}
	return false, "", fmt.Errorf("sql_db: rolelock failed to acquire lock for: %s", roleId)
}

func (d *SqlDb) acquireOrRenewRoleOnce(roleId string, nodeId string, lockDur time.Duration) (bool, string, error) {
	var curNodeId string
	var curExpires int64
	rows, err := squirrel.Select("nodeId", "expiresAt").
		From("rolelock").
		Where(squirrel.Eq{"roleId": roleId}).
		RunWith(d.db).Query()
	if err != nil {
		return false, "", fmt.Errorf("sql_db: rolelock Select failed for: %s - %v", roleId, err)
	}
	if rows.Next() {
		err = rows.Scan(&curNodeId, &curExpires)
		if err != nil {
			_ = rows.Close()
			return false, "", fmt.Errorf("sql_db: rolelock Scan failed for: %s - %v", roleId, err)
		}
	}
	err = rows.Close()
	if err != nil {
		return false, "", fmt.Errorf("sql_db: rolelock Select close failed for: %s - %v", roleId, err)
	}

	now := time.Now()
	nowMillis := common.TimeToMillis(now)
	expiresAt := common.TimeToMillis(now.Add(lockDur))

	if curNodeId != "" {

		if curExpires > nowMillis && curNodeId != nodeId {
			// lock is not expired and is held by a different node
			return false, curNodeId, nil
		}

		q := squirrel.Update("rolelock").
			SetMap(map[string]interface{}{
				"nodeId":    nodeId,
				"expiresAt": expiresAt,
			}).Where(squirrel.And{
			squirrel.Eq{"roleId": roleId},
			squirrel.Or{
				squirrel.Lt{"expiresAt": nowMillis},
				squirrel.And{squirrel.Eq{"nodeId": nodeId}, squirrel.Eq{"expiresAt": curExpires}},
			},
		})

		result, err := q.RunWith(d.db).Exec()
		if err != nil {
			if sqliteErr(err, sqlite3.ErrLocked) {
				// hack to workaround "table is locked" error when using sqlite3 - outer call will retry
				return false, "", nil
			}
			return false, "", fmt.Errorf("sql_db: update failed for roleId: %s err: %v", roleId, err)
		}
		rows, err := result.RowsAffected()

		if err != nil {
			return false, "", fmt.Errorf("sql_db: RowsAffected failed for roleId: %s err: %v", roleId, err)
		}
		if rows == 1 {
			return true, nodeId, nil
		}
	}

	insertQ := squirrel.Insert("rolelock").
		Columns("roleId", "nodeId", "expiresAt").Values(roleId, nodeId, expiresAt)
	_, err = insertQ.RunWith(d.db).Exec()
	if err == nil {
		return true, nodeId, nil
	} else {
		// retry: nodeId and err are nil/empty
		return false, "", nil
	}
}

func (d *SqlDb) ReleaseRole(roleId string, nodeId string) error {
	_, err := squirrel.Delete("rolelock").Where(squirrel.Eq{"roleId": roleId}).
		Where(squirrel.Eq{"nodeId": nodeId}).RunWith(d.db).Exec()
	if err != nil {
		err = errors.Wrap(err, "ReleaseRole failed for roleId: "+roleId+" nodeId: "+nodeId)
	}
	return err
}

func (d *SqlDb) ReleaseAllRoles(nodeId string) error {
	_, err := squirrel.Delete("rolelock").Where(squirrel.Eq{"nodeId": nodeId}).RunWith(d.db).Exec()
	if err != nil {
		err = errors.Wrap(err, "ReleaseAllRoles failed for nodeId: "+nodeId)
	}
	return err
}

func (d *SqlDb) PutComponent(component v1.Component) (int64, error) {
	now := common.NowMillis()
	previousVersion := component.Version
	component.Version += 1
	component.ModifiedAt = now
	jsonVal, err := json.Marshal(component)
	if err != nil {
		return 0, fmt.Errorf("PutComponent unable to marshal JSON: %v", err)
	}

	if previousVersion == 0 {
		err = d.insertRow("component", component.Name, component,
			[]string{"name", "projectName", "version", "createdAt", "modifiedAt", "json"},
			[]interface{}{component.Name, component.ProjectName, 1, now, now, jsonVal})
	} else {
		err = d.updateRow("component", component.Name, previousVersion, map[string]interface{}{
			"modifiedAt": now,
			"json":       jsonVal,
			"version":    component.Version,
		})
	}

	if err != nil {
		return 0, err
	}
	return component.Version, nil
}

func (d *SqlDb) GetComponent(componentName string) (component v1.Component, err error) {
	err = d.getRow("component", componentName, &component)
	return
}

func (d *SqlDb) ListProjects(input v1.ListProjectsInput) (v1.ListProjectsOutput, error) {
	componentCounts, err := d.countRowsByProject(input.NamePrefix, "component")
	if err != nil {
		return v1.ListProjectsOutput{}, err
	}
	eventSourceCounts, err := d.countRowsByProject(input.NamePrefix, "eventsource")
	if err != nil {
		return v1.ListProjectsOutput{}, err
	}

	projects := make([]v1.ProjectInfo, len(componentCounts))
	i := 0
	for projectName, componentCount := range componentCounts {
		projects[i] = v1.ProjectInfo{
			ProjectName:      projectName,
			ComponentCount:   componentCount,
			EventSourceCount: eventSourceCounts[projectName],
		}
		i++
	}

	return v1.ListProjectsOutput{Projects: projects}, nil
}

func (d *SqlDb) countRowsByProject(projectPrefix string, table string) (map[string]int64, error) {
	projectToCount := map[string]int64{}
	q := squirrel.Select("projectName", "count(*)").From(table).GroupBy("projectName")
	if projectPrefix != "" {
		q = q.Where(squirrel.Like{"projectName": projectPrefix + "%"})
	}
	rows, err := q.RunWith(d.db).Query()
	if err != nil {
		return projectToCount, err
	}
	defer common.CheckClose(rows, &err)
	for rows.Next() {
		var projectName sql.NullString
		var count int64
		err = rows.Scan(&projectName, &count)
		if err != nil {
			return projectToCount, fmt.Errorf("db: countRowsByProject %s scan err: %v", table, err)
		}
		if projectName.Valid {
			projectToCount[projectName.String] = count
		}
	}
	return projectToCount, err
}

func (d *SqlDb) ListComponents(input v1.ListComponentsInput) (output v1.ListComponentsOutput, err error) {
	q := squirrel.Select("json").From("component").OrderBy("name")
	if input.NamePrefix != "" {
		q = q.Where(squirrel.Like{"name": input.NamePrefix + "%"})
	}
	if input.ProjectName != "" {
		q = q.Where(squirrel.Eq{"projectName": input.ProjectName})
	}

	components := make([]v1.Component, 0)
	nextToken, err := d.selectPaginated(q, input.NextToken, input.Limit, func(rows *sql.Rows) error {
		var comp v1.Component
		err := d.scanJSON(rows, &comp)
		if err != nil {
			return fmt.Errorf("ListComponents: %v", err)
		}
		components = append(components, comp)
		return nil
	})
	if err != nil {
		return v1.ListComponentsOutput{}, err
	}
	return v1.ListComponentsOutput{NextToken: nextToken, Components: components}, nil
}

func (d *SqlDb) scanJSON(rows *sql.Rows, target interface{}) error {
	var jsonVal []byte
	err := rows.Scan(&jsonVal)
	if err != nil {
		return fmt.Errorf("scanJSON err in scan: %v", err)
	}
	err = json.Unmarshal(jsonVal, target)
	if err != nil {
		return fmt.Errorf("scanJSON err in unmarshal: %v", err)
	}
	return nil
}

func (d *SqlDb) selectPaginated(q squirrel.SelectBuilder, nextToken string, limit int64,
	onRow func(rows *sql.Rows) error) (string, error) {
	offset := 0
	if nextToken != "" {
		o, err := strconv.Atoi(nextToken)
		if err == nil {
			offset = o
		}
	}

	if limit < 1 || limit > 1000 {
		limit = 1000
	}

	rows, err := q.Offset(uint64(offset)).Limit(uint64(limit + 1)).RunWith(d.db).Query()
	if err != nil {
		return "", fmt.Errorf("err in query: %v", err)
	}

	defer common.CheckClose(rows, &err)
	x := int64(0)
	for x < limit && rows.Next() {
		err = onRow(rows)
		if err != nil {
			return "", err
		}
		x++
	}

	nextToken = ""
	if rows.Next() {
		nextToken = strconv.Itoa(offset + int(x))
	}
	return nextToken, nil
}

func (d *SqlDb) RemoveComponent(componentName string) (found bool, err error) {
	_, err = squirrel.Delete("eventsource").Where(squirrel.Eq{"componentName": componentName}).RunWith(d.db).Exec()
	if err != nil {
		err = fmt.Errorf("delete eventsource by componentName failed: %s err: %v", componentName, err)
		return
	}
	return d.removeRow("component", componentName)
}

func (d *SqlDb) PutEventSource(eventSource v1.EventSource) (int64, error) {
	now := common.NowMillis()
	previousVersion := eventSource.Version
	eventSource.Version += 1
	eventSource.ModifiedAt = now
	jsonVal, err := json.Marshal(eventSource)
	if err != nil {
		return 0, fmt.Errorf("PutEventSource unable to marshal JSON: %v", err)
	}

	eventType := string(v1.GetEventSourceType(eventSource))

	if previousVersion == 0 {
		err = d.insertRow("eventsource", eventSource.Name, eventSource,
			[]string{"name", "componentName", "projectName", "type", "version", "createdAt", "modifiedAt", "json"},
			[]interface{}{eventSource.Name, eventSource.ComponentName, eventSource.ProjectName,
				eventType, 1, now, now, jsonVal})
	} else {
		err = d.updateRow("eventsource", eventSource.Name, previousVersion, map[string]interface{}{
			"modifiedAt":    now,
			"componentName": eventSource.ComponentName,
			"type":          eventType,
			"json":          jsonVal,
			"version":       eventSource.Version,
		})
	}

	if err != nil {
		return 0, err
	}
	return eventSource.Version, nil
}

func (d *SqlDb) GetEventSource(eventSourceName string) (es v1.EventSource, err error) {
	err = d.getRow("eventsource", eventSourceName, &es)
	return
}

func (d *SqlDb) RemoveEventSource(eventSourceName string) (bool, error) {
	return d.removeRow("eventsource", eventSourceName)
}

func (d *SqlDb) SetEventSourcesEnabled(eventSourceNames []string, enabled bool) (int64, error) {
	q := squirrel.Update("eventsource").
		SetMap(map[string]interface{}{
			"enabled": boolToInt(enabled),
		}).Where(squirrel.Eq{"name": eventSourceNames})

	result, err := q.RunWith(d.db).Exec()
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (d *SqlDb) ListEventSources(input v1.ListEventSourcesInput) (v1.ListEventSourcesOutput, error) {
	q := squirrel.Select("json", "enabled").From("eventsource").OrderBy("name")
	if input.NamePrefix != "" {
		q = q.Where(squirrel.Like{"name": input.NamePrefix + "%"})
	}
	if input.ComponentName != "" {
		q = q.Where(squirrel.Eq{"componentName": input.ComponentName})
	}
	if input.ProjectName != "" {
		q = q.Where(squirrel.Eq{"projectName": input.ProjectName})
	}
	if input.EventSourceType != "" {
		q = q.Where(squirrel.Eq{"type": string(input.EventSourceType)})
	}

	eventSources := make([]v1.EventSourceWithStatus, 0)
	nextToken, err := d.selectPaginated(q, input.NextToken, input.Limit, func(rows *sql.Rows) error {
		var es v1.EventSource
		var enabled bool
		var jsonVal []byte
		err := rows.Scan(&jsonVal, &enabled)
		if err != nil {
			return fmt.Errorf("ListEventSources: err in rows.scan: %v", err)
		}
		err = json.Unmarshal(jsonVal, &es)
		if err != nil {
			return fmt.Errorf("ListEventSources: err in unmarshal: %v", err)
		}
		eventSources = append(eventSources, v1.EventSourceWithStatus{EventSource: es, Enabled: enabled})
		return nil
	})
	if err != nil {
		return v1.ListEventSourcesOutput{}, err
	}
	return v1.ListEventSourcesOutput{NextToken: nextToken, EventSources: eventSources}, nil
}

func (d *SqlDb) PutNodeStatus(status v1.NodeStatus) error {
	jsonVal, err := json.Marshal(status)
	if err != nil {
		return fmt.Errorf("PutNodeStatus unable to marshal JSON: %v", err)
	}
	q := squirrel.Update("nodestatus").
		SetMap(map[string]interface{}{
			"observedAt": status.ObservedAt,
			"json":       jsonVal,
		}).Where(squirrel.Eq{"nodeId": status.NodeId})

	result, err := q.RunWith(d.db).Exec()
	if err != nil {
		return fmt.Errorf("PutNodeStatus update failed for nodeId: %s err: %v", status.NodeId, err)
	}
	rows, err2 := result.RowsAffected()
	if err2 != nil {
		return fmt.Errorf("PutNodeStatus RowsAffected failed for nodeId: %s err: %v", status.NodeId, err)
	}
	if rows != 1 {
		conflictSql := strings.Replace(d.onConflictSql, "<TARGET>", "(nodeId)", 1)
		q := squirrel.Insert("nodestatus").
			Columns("nodeId", "observedAt", "json").
			Values(status.NodeId, status.ObservedAt, jsonVal).
			Suffix(conflictSql+"observedAt=?", status.ObservedAt)
		_, err := q.RunWith(d.db).Exec()
		if err != nil {
			return fmt.Errorf("PutNodeStatus insert failed for nodeId: %s err: %v", status.NodeId, err)
		}
	}
	return nil
}

func (d *SqlDb) ListNodeStatus() ([]v1.NodeStatus, error) {
	q := squirrel.Select("json").From("nodestatus").OrderBy("nodeId")
	nodes := make([]v1.NodeStatus, 0)
	rows, err := q.RunWith(d.db).Query()
	if err != nil {
		return nil, err
	}
	defer common.CheckClose(rows, &err)
	for rows.Next() {
		var node v1.NodeStatus
		err = d.scanJSON(rows, &node)
		if err != nil {
			return nil, fmt.Errorf("ListNodeStatus: %v", err)
		}
		nodes = append(nodes, node)
	}

	return nodes, nil
}

func (d *SqlDb) RemoveNodeStatusOlderThan(observedAt time.Time) (int64, error) {
	del := squirrel.Delete("nodestatus").Where(squirrel.Lt{"observedAt": common.TimeToMillis(observedAt)})
	return d.removeRows(del)
}

func (d *SqlDb) RemoveNodeStatus(nodeId string) (bool, error) {
	return d.removeRowByColumn("nodestatus", "nodeId", nodeId)
}

func (d *SqlDb) insertRow(table string, name string, val interface{}, columns []string, bindVals []interface{}) error {
	q := squirrel.Insert(table).Columns(columns...).Values(bindVals...)
	if d.DebugLog {
		log.Debug("sql_db: insertRow", "sql", squirrel.DebugSqlizer(q))
	}

	_, err := q.RunWith(d.db).Exec()
	if err != nil {
		var count int64
		err2 := squirrel.Select("count(*)").From(table).Where(squirrel.Eq{"name": name}).
			RunWith(d.db).QueryRow().Scan(&count)
		if err2 == nil && count > 0 {
			return AlreadyExists
		}
		return fmt.Errorf("insertRow failed for table: %s name: %s err: %v", table, name, err)
	}
	return nil
}

func (d *SqlDb) updateRow(table string, name string, previousVersion int64, updates map[string]interface{}) error {
	q := squirrel.Update(table).
		SetMap(updates).
		Where(squirrel.Eq{"name": name, "version": previousVersion})
	if d.DebugLog {
		log.Debug("sql_db: updateRow", "sql", squirrel.DebugSqlizer(q))
	}
	result, err := q.RunWith(d.db).Exec()
	if err != nil {
		return fmt.Errorf("updateRow failed for table: %s name: %s err: %v", table, name, err)
	}
	rows, err2 := result.RowsAffected()
	if err2 != nil {
		log.Warn("sql_db: updateRow RowsAffected", "err", err)
	}
	if rows != 1 {
		return IncorrectPreviousVersion
	}
	return nil
}

func (d *SqlDb) getRow(table string, name string, target interface{}) error {
	rows, err := squirrel.Select("json").
		From(table).
		Where(squirrel.Eq{"name": name}).
		RunWith(d.db).Query()
	if err != nil {
		return fmt.Errorf("getRow %s query failed for: %s - %v", table, name, err)
	}
	defer common.CheckClose(rows, &err)
	if rows.Next() {
		var jsonVal []byte
		err = rows.Scan(&jsonVal)
		if err == nil {
			err = json.Unmarshal(jsonVal, target)
			if err != nil {
				return fmt.Errorf("getRow %s json unmarshal failed for: %s - %v", table, name, err)
			}
			return nil
		} else {
			return fmt.Errorf("getRow %s scan failed for: %s - %v", table, name, err)
		}
	} else {
		return NotFound
	}
}

func (d *SqlDb) removeRow(table string, name string) (found bool, err error) {
	return d.removeRowByColumn(table, "name", name)
}

func (d *SqlDb) removeRowByColumn(table string, colName string, colValue string) (found bool, err error) {
	var result sql.Result
	var rows int64
	result, err = squirrel.Delete(table).Where(squirrel.Eq{colName: colValue}).RunWith(d.db).Exec()
	if err != nil {
		err = fmt.Errorf("delete %s failed: %s err: %v", table, colValue, err)
		return
	}
	rows, err = result.RowsAffected()
	if err != nil {
		err = fmt.Errorf("delete %s rowsaffected failed: %s err: %v", table, colValue, err)
		return
	}
	found = rows > 0
	return
}

func (d *SqlDb) removeRows(del squirrel.DeleteBuilder) (rows int64, err error) {
	var result sql.Result
	result, err = del.RunWith(d.db).Exec()
	if err != nil {
		err = fmt.Errorf("delete failed - err: %v", err)
		return
	}
	rows, err = result.RowsAffected()
	if err != nil {
		err = fmt.Errorf("delete rowsaffected failed - err: %v", err)
		return
	}
	return
}

func (d *SqlDb) GetComponentDeployCount(componentName string, version int64) (int, error) {
	rows, err := squirrel.Select("startCount").From("componentstart").
		Where(squirrel.Eq{"name": componentName}).
		Where(squirrel.Eq{"version": version}).
		RunWith(d.db).Query()
	if err != nil {
		return 0, err
	}
	defer common.CheckClose(rows, &err)
	var count int
	if rows.Next() {
		err = rows.Scan(&count)
		if err != nil {
			return 0, fmt.Errorf("db: GetComponentDeployCount scan err: %v", err)
		}
	}
	return count, nil
}

func (d *SqlDb) IncrementComponentDeployCount(componentName string, version int64) error {
	now := common.NowMillis()
	conflictSql := strings.Replace(d.onConflictSql, "<TARGET>", "(name, version)", 1)
	insertQ := squirrel.Insert("componentstart").
		Columns("name", "version", "startCount", "firstStartedAt", "lastStartedAt").
		Values(componentName, version, 1, now, now).
		Suffix(conflictSql+"startCount=componentstart.startCount+1, lastStartedAt=?", now)
	_, err := insertQ.RunWith(d.db).Exec()
	return err
}

func (d *SqlDb) Migrate() error {
	migrations := []darwin.Migration{
		{
			Version:     1,
			Description: "Create component table",
			Script: `create table component (
                        name          varchar(60) primary key,
                        projectName   varchar(60) not null,
                        version       int not null,
                        createdAt     bigint not null,
                        modifiedAt    bigint not null,
                        json          ` + d.blobType + ` not null
                     )`,
		},
		{
			Version:     2,
			Description: "Create eventsource table",
			Script: `create table eventsource (
                        name           varchar(60) primary key,
                        componentName  varchar(60) not null,
                        projectName    varchar(60) not null,
                        version        int not null,
                        createdAt      bigint not null,
                        modifiedAt     bigint not null,
                        type           varchar(30) not null,
                        json           ` + d.blobType + ` not null
                     )`,
		},
		{
			Version:     3,
			Description: "Create nodestatus table",
			Script: `create table nodestatus (
                        nodeId         varchar(60) primary key,
                        observedAt     bigint not null,
                        json           ` + d.blobType + ` not null
                     )`,
		},
		{
			Version:     4,
			Description: "Create rolelock table",
			Script: `create table rolelock (
                        roleId         varchar(80) primary key,
                        nodeId         varchar(60) not null,
                        expiresAt      bigint not null
                     )`,
		},
		{
			Version:     5,
			Description: "Add eventsource.enabled column",
			Script:      `alter table eventsource add column enabled int not null default 1`,
		},
		{
			Version:     6,
			Description: "Create componentstart table",
			Script: `create table componentstart (
                        name             varchar(60) not null,
                        version          int not null,
                        startCount       int not null,
                        firstStartedAt   bigint not null,
                        lastStartedAt    bigint not null,
                        primary key (name, version)
                     )`,
		},
	}
	darwinDriver := darwin.NewGenericDriver(d.db, migrationDialect(d.driver))
	m := darwin.New(darwinDriver, migrations, nil)
	err := m.Migrate()
	if err != nil {
		return fmt.Errorf("sql_db.Migrate failed: %v", err)
	}
	return nil
}

func migrationDialect(driver string) darwin.Dialect {
	if strings.Contains(driver, "sqlite") {
		return darwin.SqliteDialect{}
	} else if strings.Contains(driver, "postgres") {
		return darwin.PostgresDialect{}
	}
	return darwin.MySQLDialect{}
}

func sqliteErr(err error, num sqlite3.ErrNo) bool {
	if err != nil {
		serr, ok := err.(sqlite3.Error)
		if ok && serr.Code == num {
			return true
		}
	}
	return false
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func panicOnErr(err error) {
	if err != nil {
		panic(err)
	}
}
