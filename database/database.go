package database

import (
	"context"
	"database/sql"
	"github.com/Dri0m/flashpoint-submission-system/constants"
	"github.com/Dri0m/flashpoint-submission-system/types"
	"github.com/Dri0m/flashpoint-submission-system/utils"
	"github.com/sirupsen/logrus"
	"os"
	"strings"
	"time"
)

type DB struct {
	Conn *sql.DB
}

// OpenDB opens DB or panics
func OpenDB(l *logrus.Logger) *sql.DB {
	l.Infof("opening database '%s'...", constants.DbName)
	db, err := sql.Open("sqlite3", constants.DbName+"?cache=shared")
	if err != nil {
		l.Fatal(err)
	}
	err = db.Ping()
	if err != nil {
		l.Fatal(err)
	}

	db.SetMaxOpenConns(0)

	_, err = db.Exec(`PRAGMA journal_mode = WAL`)
	if err != nil {
		l.Fatal(err)
	}

	file, err := os.ReadFile("sql.sql")
	if err != nil {
		l.Fatal(err)
	}

	_, err = db.Exec(string(file))
	if err != nil {
		l.Fatal(err)
	}

	return db
}

// StoreSession store session into the DB with set expiration date
func (db *DB) StoreSession(ctx context.Context, key string, uid int64, durationSeconds int64) error {
	expiration := time.Now().Add(time.Second * time.Duration(durationSeconds)).Unix()
	_, err := db.Conn.ExecContext(ctx, `INSERT INTO session (secret, uid, expires_at) VALUES (?, ?, ?)`, key, uid, expiration)
	return err
}

// DeleteSession deletes specific session
func (db *DB) DeleteSession(ctx context.Context, secret string) error {
	_, err := db.Conn.ExecContext(ctx, `DELETE FROM session WHERE secret=?`, secret)
	return err
}

// GetUIDFromSession returns user ID and/or expiration state
func (db *DB) GetUIDFromSession(ctx context.Context, key string) (string, bool, error) {
	row := db.Conn.QueryRowContext(ctx, `SELECT uid, expires_at FROM session WHERE secret=?`, key)

	var uid string
	var expiration int64
	err := row.Scan(&uid, &expiration)
	if err != nil {
		return "", false, err
	}

	if expiration <= time.Now().Unix() {
		return "", false, nil
	}

	return uid, true, nil
}

// StoreDiscordUser store discord user or replace with new data
func (db *DB) StoreDiscordUser(ctx context.Context, discordUser *types.DiscordUser) error {
	_, err := db.Conn.ExecContext(ctx,
		`INSERT OR REPLACE INTO discord_user (id, username, avatar, discriminator, public_flags, flags, locale, mfa_enabled) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		discordUser.ID, discordUser.Username, discordUser.Avatar, discordUser.Discriminator, discordUser.PublicFlags, discordUser.Flags, discordUser.Locale, discordUser.MFAEnabled)
	return err
}

// GetDiscordUser returns DiscordUserResponse
func (db *DB) GetDiscordUser(ctx context.Context, uid int64) (*types.DiscordUser, error) {
	row := db.Conn.QueryRowContext(ctx, `SELECT username, avatar, discriminator, public_flags, flags, locale, mfa_enabled FROM discord_user WHERE id=?`, uid)

	discordUser := &types.DiscordUser{ID: uid}
	err := row.Scan(&discordUser.Username, &discordUser.Avatar, &discordUser.Discriminator, &discordUser.PublicFlags, &discordUser.Flags, &discordUser.Locale, &discordUser.MFAEnabled)
	if err != nil {
		return nil, err
	}

	return discordUser, nil
}

// StoreDiscordUserAuthorization stores discord user auth state
func (db *DB) StoreDiscordUserAuthorization(ctx context.Context, uid int64, isAuthorized bool) error {
	a := 0
	if isAuthorized {
		a = 1
	}

	_, err := db.Conn.ExecContext(ctx, `INSERT OR REPLACE INTO authorization (fk_uid, authorized) VALUES (?, ?)`, uid, a)
	return err
}

// IsDiscordUserAuthorized returns discord user auth state
func (db *DB) IsDiscordUserAuthorized(ctx context.Context, uid int64) (bool, error) {
	row := db.Conn.QueryRowContext(ctx, `SELECT authorized FROM authorization WHERE fk_uid=?`, uid)
	var a int64
	err := row.Scan(&a)
	if err != nil {
		return false, err
	}

	if a == 1 {
		return true, nil
	}
	return false, nil
}

// StoreSubmission stores plain submission
func (db *DB) StoreSubmission(ctx context.Context, tx *sql.Tx) (int64, error) {
	res, err := tx.ExecContext(ctx, `INSERT INTO submission DEFAULT VALUES`)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	return id, nil
}

// StoreSubmissionFile stores submission file
func (db *DB) StoreSubmissionFile(ctx context.Context, tx *sql.Tx, s *types.SubmissionFile) (int64, error) {
	res, err := tx.ExecContext(ctx, `INSERT INTO submission_file (fk_uploader_id, fk_submission_id, original_filename, current_filename, size, uploaded_at) VALUES (?, ?, ?, ?, ?, ?)`,
		s.SubmitterID, s.SubmissionID, s.OriginalFilename, s.CurrentFilename, s.Size, s.UploadedAt.Unix())
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	return id, nil
}

// SearchSubmissions returns extended submissions based on given filter
func (db *DB) SearchSubmissions(ctx context.Context, filter *types.SubmissionsFilter) ([]*types.ExtendedSubmission, error) {
	filters := make([]string, 0)
	data := make([]interface{}, 0)

	data = append(data, constants.ValidatorID, constants.ValidatorID)

	if filter != nil {
		if filter.SubmissionID != nil {
			filters = append(filters, "submission.id=?")
			data = append(data, *filter.SubmissionID)
		}
		if filter.SubmitterID != nil {
			filters = append(filters, "uploader.id=?")
			data = append(data, *filter.SubmitterID)
		}
	}

	where := ""
	if len(filters) > 0 {
		where = " WHERE "
	}

	rows, err := db.Conn.QueryContext(ctx, `
		SELECT submission.id AS submission_id, 
			uploader.id AS uploader_id, uploader.username AS uploader_username, uploader.avatar AS uploader_avatar,
			updater.id AS updater_id, updater.username AS updater_username, updater.avatar AS updater_avatar,
			files.submission_file_id, files.original_filename, files.current_filename, files.size, 
			files.uploaded_at, files.updated_at,
			meta.title, meta.alternate_titles, meta.launch_command,
			bot_comment.action as bot_action,
			latest_action.action as latest_action
		FROM submission
		
		LEFT JOIN 
			(SELECT submission.id AS submission_id, 
					oldest.fk_uploader_id AS uploader_id, newest.fk_uploader_id AS updater_id, 
					newest.id AS submission_file_id, newest.original_filename, newest.current_filename, newest.size, 
					oldest.uploaded_at AS uploaded_at, newest.uploaded_at AS updated_at, 
					MIN(oldest.uploaded_at), MAX(newest.uploaded_at) FROM submission 
				LEFT JOIN submission_file oldest ON oldest.fk_submission_id=submission.id
				LEFT JOIN submission_file newest ON newest.fk_submission_id=submission.id
				GROUP BY submission.id) 
			AS files ON files.submission_id=submission.id
		LEFT JOIN discord_user uploader ON files.uploader_id = uploader.id
		LEFT JOIN discord_user updater ON files.updater_id = updater.id
		LEFT JOIN curation_meta meta ON meta.fk_submission_file_id = files.submission_file_id
		LEFT JOIN 
			(SELECT submission.id AS submission_id, (SELECT name FROM "action" WHERE id=comment.fk_action_id) as action
				FROM submission LEFT JOIN comment ON comment.fk_submission_id=submission.id
				WHERE comment.fk_author_id=?) 
			AS bot_comment ON bot_comment.submission_id=submission.id
		LEFT JOIN 
			(SELECT submission.id AS submission_id, comment.created_at, (SELECT name FROM "action" WHERE id=comment.fk_action_id) as action
				FROM submission LEFT JOIN comment ON comment.fk_submission_id=submission.id
				WHERE fk_action_id!=(SELECT id FROM "action" WHERE name="comment")
				AND comment.fk_author_id!=?
				GROUP BY submission.id
				HAVING MAX(comment.created_at)) 
			AS latest_action ON latest_action.submission_id=submission.id
		`+where+strings.Join(filters, " AND ")+`
		GROUP BY submission.id
		ORDER BY files.updated_at DESC`, data...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make([]*types.ExtendedSubmission, 0)

	var uploadedAt int64
	var updatedAt int64
	var submitterAvatar string
	var updaterAvatar string

	for rows.Next() {
		s := &types.ExtendedSubmission{}
		if err := rows.Scan(
			&s.SubmissionID,
			&s.SubmitterID, &s.SubmitterUsername, &submitterAvatar,
			&s.UpdaterID, &s.UpdaterUsername, &updaterAvatar,
			&s.FileID, &s.OriginalFilename, &s.CurrentFilename, &s.Size,
			&uploadedAt, &updatedAt,
			&s.CurationTitle, &s.CurationAlternateTitles, &s.CurationLaunchCommand,
			&s.BotAction,
			&s.LatestAction); err != nil {
			return nil, err
		}
		s.SubmitterAvatarURL = utils.FormatAvatarURL(s.SubmitterID, submitterAvatar)
		s.UpdaterAvatarURL = utils.FormatAvatarURL(s.UpdaterID, updaterAvatar)
		s.UploadedAt = time.Unix(uploadedAt, 0)
		s.UpdatedAt = time.Unix(updatedAt, 0)
		result = append(result, s)
	}

	return result, nil
}

// StoreCurationMeta stores curation meta
func (db *DB) StoreCurationMeta(ctx context.Context, tx *sql.Tx, cm *types.CurationMeta) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO curation_meta (fk_submission_file_id, application_path, developer, extreme, game_notes, languages,
                           launch_command, original_description, play_mode, platform, publisher, release_date, series, source, status,
                           tags, tag_categories, title, alternate_titles, library, version, curation_notes, mount_parameters) 
                           VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		cm.SubmissionFileID, cm.ApplicationPath, cm.Developer, cm.Extreme, cm.GameNotes, cm.Languages,
		cm.LaunchCommand, cm.OriginalDescription, cm.PlayMode, cm.Platform, cm.Publisher, cm.ReleaseDate, cm.Series, cm.Source, cm.Status,
		cm.Tags, cm.TagCategories, cm.Title, cm.AlternateTitles, cm.Library, cm.Version, cm.CurationNotes, cm.MountParameters)
	return err
}

// GetCurationMetaBySubmissionFileID returns curation meta for given submission file
func (db *DB) GetCurationMetaBySubmissionFileID(ctx context.Context, sfid int64) (*types.CurationMeta, error) {
	row := db.Conn.QueryRowContext(ctx, `SELECT submission_file.fk_submission_id, application_path, developer, extreme, game_notes, languages,
                           launch_command, original_description, play_mode, platform, publisher, release_date, series, source, status,
                           tags, tag_categories, title, alternate_titles, library, version, curation_notes, mount_parameters 
		FROM curation_meta JOIN submission_file ON curation_meta.fk_submission_file_id = submission_file.id
		WHERE fk_submission_file_id=?`, sfid, sfid)

	c := &types.CurationMeta{SubmissionFileID: sfid}
	err := row.Scan(&c.SubmissionID, &c.ApplicationPath, &c.Developer, &c.Extreme, &c.GameNotes, &c.Languages,
		&c.LaunchCommand, &c.OriginalDescription, &c.PlayMode, &c.Platform, &c.Publisher, &c.ReleaseDate, &c.Series, &c.Source, &c.Status,
		&c.Tags, &c.TagCategories, &c.Title, &c.AlternateTitles, &c.Library, &c.Version, &c.CurationNotes, &c.MountParameters)
	if err != nil {
		return nil, err
	}

	return c, nil
}

// StoreComment stores curation meta
func (db *DB) StoreComment(ctx context.Context, tx *sql.Tx, c *types.Comment) error {
	var msg *string
	if c.Message != nil {
		s := strings.TrimSpace(*c.Message)
		msg = &s
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO comment (fk_author_id, fk_submission_id, message, fk_action_id, created_at) 
                           VALUES (?, ?, ?, (SELECT id FROM "action" WHERE name=?), ?)`,
		c.AuthorID, c.SubmissionID, msg, c.Action, c.CreatedAt.Unix())
	return err
}

// GetExtendedCommentsBySubmissionID returns all comments with author data for a given submission
func (db *DB) GetExtendedCommentsBySubmissionID(ctx context.Context, sid int64) ([]*types.ExtendedComment, error) {
	rows, err := db.Conn.QueryContext(ctx, `
		SELECT discord_user.id, username, avatar, message, (SELECT name FROM "action" WHERE id=comment.fk_action_id) as action, created_at 
		FROM comment 
		JOIN discord_user ON discord_user.id = fk_author_id
		WHERE fk_submission_id=? 
		ORDER BY created_at;`, sid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make([]*types.ExtendedComment, 0)

	var createdAt int64
	var avatar string
	var message *string

	for rows.Next() {

		ec := &types.ExtendedComment{SubmissionID: sid}
		if err := rows.Scan(&ec.AuthorID, &ec.Username, &avatar, &message, &ec.Action, &createdAt); err != nil {
			return nil, err
		}
		ec.CreatedAt = time.Unix(createdAt, 0)
		ec.AvatarURL = utils.FormatAvatarURL(ec.AuthorID, avatar)
		if message != nil {
			ec.Message = strings.Split(*message, "\n")
		}
		result = append(result, ec)
	}

	return result, nil
}
