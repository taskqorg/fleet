package main

import (
	"testing"

	"fleet/contract"
)

// TestBotAvatarSetAndClear: an image of the bot's project becomes its avatar with a still frame and glow; another
// project's file, a non-image and a bad colour are refused; clear returns the bot to its letter.
func TestBotAvatarSetAndClear(t *testing.T) {
	e := openTestCoordinator(t, false)
	member := map[string]string{"Authorization": "Bearer " + e.browserSecret, "X-Fleet-Person": "member@trial.test"}
	botID := newID()
	code, resp := e.callJSON(t, "POST", "/api/ops/bot_settings_save", contract.BotSettingsSaveRequest{
		BotId: botID, ProjectId: "desiign", BaseRevision: 0, Settings: testBotSettings("pm"),
	}, member)
	if code != 200 {
		t.Fatalf("bot_settings_save = %d %v", code, resp)
	}
	if _, err := e.ownerStore.pool.Exec(e.ctx, `
INSERT INTO artifact(id,project_id,scope,content_hash,size,media_type,name,state) VALUES
 ('av-loop','desiign','prod','h-av-loop',42,'image/svg+xml','eye.svg','stored'),
 ('av-still','desiign','prod','h-av-still',42,'image/png','eye.png','stored'),
 ('av-text','desiign','prod','h-av-text',42,'text/plain','eye.txt','stored'),
 ('av-other','fleet','prod','h-av-other',42,'image/png','other.png','stored')`); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []doc{
		{"botId": botID, "artifactId": "av-other"},
		{"botId": botID, "artifactId": "av-text"},
		{"botId": botID, "artifactId": "av-loop", "stillArtifactId": "av-text"},
		{"botId": botID, "artifactId": "av-loop", "glow": "red"},
	} {
		if code, resp := e.callJSON(t, "POST", "/api/ops/bot_avatar_set", bad, member); code != 400 {
			t.Fatalf("bot_avatar_set %v = %d %v, want 400", bad, code, resp)
		}
	}
	code, resp = e.callJSON(t, "POST", "/api/ops/bot_avatar_set", doc{"botId": botID, "artifactId": "av-loop", "stillArtifactId": "av-still", "glow": "#FF2A1A"}, member)
	if code != 200 {
		t.Fatalf("bot_avatar_set = %d %v", code, resp)
	}
	av := mapOf(mapOf(resp["bot"])["avatar"])
	if astr(av["artifactId"]) != "av-loop" || astr(av["stillArtifactId"]) != "av-still" || astr(av["mediaType"]) != "image/svg+xml" || astr(av["glow"]) != "#FF2A1A" {
		t.Fatalf("avatar = %v", av)
	}
	code, resp = e.callJSON(t, "POST", "/api/ops/bot_list", doc{"projectId": "desiign"}, member)
	if code != 200 {
		t.Fatalf("bot_list = %d %v", code, resp)
	}
	found := false
	for _, b := range resp["bots"].([]any) {
		if m := mapOf(b); astr(m["id"]) == botID {
			found = astr(mapOf(m["avatar"])["artifactId"]) == "av-loop" && m["needsYou"] == false
		}
	}
	if !found {
		t.Fatalf("bot_list lacks the avatar: %v", resp)
	}
	code, resp = e.callJSON(t, "POST", "/api/ops/bot_avatar_clear", doc{"botId": botID}, member)
	if code != 200 || mapOf(resp["bot"])["avatar"] != nil {
		t.Fatalf("bot_avatar_clear = %d %v", code, resp)
	}
}
