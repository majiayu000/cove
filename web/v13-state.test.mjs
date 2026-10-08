import { test } from "node:test";
import assert from "node:assert/strict";
import { clientDisplay, gatewayDisplay, keyDisplay, routeMemberDisplay, mergeRequestPages } from "./src/v13-state.ts";

const source = { enabled: true, credential_configured: true, auth_status: "configured", account_id: "a", models: ["m"], max_concurrent: 2, verification: { status: "untested" } };
const status = { storage_healthy: true, secrets_healthy: true, accepting_requests: true, account_active: { a: 1 } };

test("a running local gateway never implies a verified upstream", () => {
  const empty = gatewayDisplay(status, []);
  assert.equal(empty.gatewayZh, "本机服务正常");
  assert.match(empty.gatewaySubZh, /尚未添加/);
  assert.equal(empty.gatewayDot, "var(--warn)");
  assert.match(gatewayDisplay(status, [source]).gatewaySubZh, /尚无来源测试通过/);
  assert.equal(gatewayDisplay({ ...status, storage_healthy: false }, [source]).gatewayDot, "var(--err)");
  assert.match(gatewayDisplay({ ...status, secrets_healthy: false }, [source]).gatewayZh, /凭据存储异常/);
  assert.match(gatewayDisplay({ ...status, accepting_requests: false }, [source]).gatewayZh, /暂停/);
  assert.equal(gatewayDisplay({ ...status, runtime_stale: true }, [source]).gatewayZh, "本机状态未知");
  assert.equal(gatewayDisplay({}, []).gatewayZh, "本机状态未知");
  const verified = {...source, verification:{status:"passed",tested_at:"2026-10-03T00:00:00Z"}};
  assert.equal(gatewayDisplay(status, [verified]).gatewayDot, "var(--ok)");
  assert.equal(gatewayDisplay(status, [{...verified, enabled:false}]).gatewayDot, "var(--warn)");
  assert.equal(gatewayDisplay({...status, sources_stale:true}, [verified]).gatewayDot, "var(--warn)");
});

test("configured client means checked selected fields, never an inferred connected runtime", () => {
  const card = {status:"installed", configuration:{state:"configured",model:"coding",path:"/selected/config.toml",reason:"文件匹配；运行未验证"}};
  assert.equal(clientDisplay(card).val,"coding");
  assert.equal(clientDisplay(card).path,"/selected/config.toml");
  assert.equal(clientDisplay(card).stZh,"已写配置");
  assert.equal(clientDisplay(card).stEn,"Applied");
  for(const state of ["modified","restored","unavailable","applying","partial"]){
    const shown=clientDisplay({...card,configuration:{...card.configuration,state}});
    assert.equal(shown.currentModel,"");
    assert.notEqual(shown.dot,"var(--ok)");
  }
  assert.equal(clientDisplay({...card,status:"unsupported_version"}).stZh,"版本未核验");
  assert.equal(clientDisplay({...card,status:"not_installed"}).stZh,"未安装");
  assert.equal(clientDisplay({status:"installed"},true).val,"Choose model");
});

test("manual clients expose setup guidance and remain unverified", () => {
  const card = {status:"manual_setup",manual_setup:"Follow the official setup guide",configuration:{state:"not_configured"}};
  const shown = clientDisplay(card);
  assert.equal(shown.stZh,"接入待核验");
  assert.equal(shown.stEn,"Setup unverified");
  assert.equal(shown.title,card.manual_setup);
  assert.equal(shown.currentModel,"");
  assert.equal(shown.dot,"var(--warn)");
});

test("route slots use shared-account counts and actual exclusion reasons", () => {
  const member={model_id:"model"};
  const route={enabled:true,runtime:{candidates:[{model_id:"model",eligible:false,reason:"账号认证被上游拒绝"}]}};
  const shown=routeMemberDisplay(route,member,source,status);
  assert.equal(shown.slot,"1/2");
  assert.equal(shown.h,"已排除");
  assert.match(shown.title,/账号认证被上游拒绝/);
  assert.notEqual(shown.dot,"var(--ok)");
  assert.equal(routeMemberDisplay(route,member,source,{...status,runtime_stale:true}).slot,"—");
  assert.equal(routeMemberDisplay(route,member,{...source, max_concurrent:null},{...status,account_active:{}}).slot,"0/∞");
  assert.equal(routeMemberDisplay({enabled:true},member,source,status).h,"未知");
  assert.equal(routeMemberDisplay({enabled:false},member,source,status).h,"已停用");
  const untested={enabled:true,runtime:{candidates:[{model_id:"model",eligible:true,reason:"资格通过"}]}};
  assert.equal(routeMemberDisplay(untested,member,source,status).h,"待验证");
  const passed={...source,verification:{status:"passed"}};
  assert.equal(routeMemberDisplay(untested,member,passed,status).dot,"var(--ok)");
  assert.equal(routeMemberDisplay(untested,member,passed,{...status,sources_stale:true}).dot,"var(--warn)");
});

test("expired keys never look active; revocation and disabling take precedence", () => {
  const now=Date.parse("2026-10-03T00:00:00Z");
  const key={expires_at:"2026-10-02T00:00:00Z",enabled:true};
  assert.equal(keyDisplay(key,now).active,false);
  assert.equal(keyDisplay(key,now).stZh,"已过期");
  assert.equal(keyDisplay({...key,revoked:true},now).stZh,"已撤销");
  assert.equal(keyDisplay({...key,enabled:false},now).stZh,"已停用");
  assert.equal(keyDisplay({expires_at:null},now).active,true);
  assert.equal(keyDisplay({expires_at:"2026-10-04T00:00:00Z"},now).active,true);
  assert.equal(keyDisplay({revoke_at:"2026-10-02T00:00:00Z"},now).active,false);
  assert.equal(keyDisplay({revoke_at:"2026-10-02T00:00:00Z"},now).stZh,"已失效");
  assert.equal(keyDisplay({revoke_at:"2026-10-04T00:00:00Z"},now).active,true);
  assert.equal(keyDisplay({...key,enabled:false},now).dot,"var(--ink3)");
});

test("overlapping request pages retain current records and stable order", () => {
  const first=[{id:"a",status:"succeeded"},{id:"b",status:"streaming"}];
  const next=[{id:"b",status:"dispatching"},{id:"c"},{id:"c"},{id:"d"}];
  const merged=mergeRequestPages(first,next);
  assert.deepEqual(merged.map(r=>r.id),["a","b","c","d"]);
  assert.equal(merged[1].status,"streaming");
  assert.equal(first.length,2);
  assert.equal(next.length,4);
});
