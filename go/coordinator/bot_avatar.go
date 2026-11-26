// A bot's avatar: an image or video artifact shown in place of its letter (bot_avatar_set, bot_avatar_clear).
package main

import (
	"encoding/json"
	"regexp"
	"strings"

	"fleet/contract"
	"fleet/coordinator/db"
)

func init() {
	op("bot_avatar_set", botAvatarSet)
	op("bot_avatar_clear", botAvatarClear)
	op("bot_identity_set", botIdentitySet)
}

// botIdentitySet: the bot's own identity colour and pattern (OR-72); '' returns to the automatic colour and a solid bar.
func botIdentitySet(q *Request, r *contract.BotIdentitySetRequest) (contract.BotIdentitySetReply, error) {
	var out contract.BotIdentitySetReply
	b, err := botAvatarChanger(q, r.BotId)
	if err != nil {
		return out, err
	}
	if err := identityCheck("bot", map[string]json.RawMessage{"color": jsonBytes(r.Color), "pattern": jsonBytes(r.Pattern)}); err != nil {
		return out, err
	}
	n, err := q.db().BotIdentitySet(q.ctx(), db.BotIdentitySetParams{ID: b.Id, IdentityColor: r.Color, IdentityPattern: r.Pattern})
	if err != nil {
		return out, err
	}
	if n != 1 {
		return out, errNotFound
	}
	type identity struct {
		Color   string `json:"color"`
		Pattern string `json:"pattern"`
	}
	if err := audit(q, "bot:"+b.Id, "identity", identity{deref(b.Color), deref(b.Pattern)}, identity{r.Color, r.Pattern}, auditExtra{ProjectID: &b.ProjectId}); err != nil {
		return out, err
	}
	out.Bot, err = readBot(q, r.BotId)
	return out, err
}

var glowPattern = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

// botAvatarChanger: the bot's director or a full-access person; an agent acts within the bot's authority, as
// bot_task_link does.
func botAvatarChanger(q *Request, id string) (botFull, error) {
	b, err := readBotFull(q, id)
	if err != nil {
		return b, err
	}
	if q.agent != "" {
		return b, botAuthorityCheck(q, &b)
	}
	return b, botDirects(q, b.BotRecord)
}

// avatarArtifact: a stored image or video of the bot's project. Agent output is untrusted: the browser shows it only
// through /api/results (sandboxed, no scripts) in <img> or <video>.
func avatarArtifact(q *Request, id, projectID string) (*Artifact, error) {
	x, err := artifactRow(withTx(q), q.app, id)
	if err != nil {
		return nil, err
	}
	if x.State != "stored" {
		return nil, fail(400, "The avatar file is not stored yet")
	}
	if x.ProjectID == nil || *x.ProjectID != projectID {
		return nil, fail(400, "The avatar file belongs to another project")
	}
	if !strings.HasPrefix(x.MediaType, "image/") && !strings.HasPrefix(x.MediaType, "video/") {
		return nil, fail(400, "An avatar is an image or a video, not "+x.MediaType)
	}
	return x, nil
}

func botAvatarSet(q *Request, r *contract.BotAvatarSetRequest) (contract.BotAvatarSetReply, error) {
	var out contract.BotAvatarSetReply
	b, err := botAvatarChanger(q, r.BotId)
	if err != nil {
		return out, err
	}
	if _, err := avatarArtifact(q, r.ArtifactId, b.ProjectId); err != nil {
		return out, err
	}
	var still *string
	if r.StillArtifactId != nil && *r.StillArtifactId != "" {
		x, err := avatarArtifact(q, *r.StillArtifactId, b.ProjectId)
		if err != nil {
			return out, err
		}
		if !strings.HasPrefix(x.MediaType, "image/") {
			return out, fail(400, "A still frame is an image")
		}
		still = &x.ID
	}
	glow := deref(r.Glow)
	if glow != "" && !glowPattern.MatchString(glow) {
		return out, fail(400, "Glow is a colour as #RRGGBB")
	}
	if err := writeBotAvatar(q, b, &r.ArtifactId, still, glow); err != nil {
		return out, err
	}
	out.Bot, err = readBot(q, r.BotId)
	return out, err
}

func botAvatarClear(q *Request, r *contract.BotAvatarClearRequest) (contract.BotAvatarClearReply, error) {
	var out contract.BotAvatarClearReply
	b, err := botAvatarChanger(q, r.BotId)
	if err != nil {
		return out, err
	}
	if b.Avatar != nil {
		if err := writeBotAvatar(q, b, nil, nil, ""); err != nil {
			return out, err
		}
	}
	out.Bot, err = readBot(q, r.BotId)
	return out, err
}

func writeBotAvatar(q *Request, b botFull, artifact, still *string, glow string) error {
	n, err := q.db().BotAvatarSet(q.ctx(), db.BotAvatarSetParams{ID: b.Id, AvatarArtifactID: artifact, AvatarStillArtifactID: still, AvatarGlow: glow})
	if err != nil {
		return err
	}
	if n != 1 {
		return errNotFound
	}
	var prev any
	if b.Avatar != nil {
		prev = b.Avatar.ArtifactId
	}
	return audit(q, "bot:"+b.Id, "avatar", prev, artifact, auditExtra{ProjectID: &b.ProjectId})
}

// botAvatarOf: the contract avatar of a bot row; nil when the bot shows its letter.
func botAvatarOf(artifact, mediaType, still *string, glow string) *contract.BotAvatar {
	if artifact == nil {
		return nil
	}
	return &contract.BotAvatar{ArtifactId: *artifact, MediaType: deref(mediaType), StillArtifactId: still, Glow: glow}
}
