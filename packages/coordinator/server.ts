import {fileURLToPath} from 'node:url';
import {createApp} from './app.js';
import {createStore} from './store.js';
// FLEET_DATABASE_URL: the coordinator role (fleet_app on PDS18, e.g. postgresql://fleet_app@/fleet?host=/var/run/postgresql).
// FLEET_MIGRATION_DATABASE_URL: the owner role (fleet) that applies db/schema.sql on start; defaults to FLEET_DATABASE_URL.
const database=process.env.FLEET_DATABASE_URL;if(!database)throw Error('FLEET_DATABASE_URL required');
const owner=createStore(process.env.FLEET_MIGRATION_DATABASE_URL||database);await owner.migrate();await owner.close();
const store=createStore(database);
const app=createApp({appDir:process.env.FLEET_APP_DIR,ownerEmail:process.env.FLEET_ACCESS_OWNER,changeLogRetentionDays:Number(process.env.FLEET_CHANGE_LOG_RETENTION_DAYS||7),nativeHostIds:(process.env.FLEET_NATIVE_HOST_IDS||'').split(',').map(s=>s.trim()).filter(Boolean),defaultAccountBinding:process.env.FLEET_DEFAULT_ACCOUNT_BINDING,testSiteRoot:process.env.FLEET_TEST_SITE_ROOT,integrationHostId:process.env.FLEET_INTEGRATION_HOST_ID,publicationHostId:process.env.FLEET_PUBLICATION_HOST_ID,projectConfigDirectory:process.env.FLEET_PROJECT_CONFIG_DIRECTORY||fileURLToPath(new URL('../../config/',import.meta.url)),pipeConnectionFile:process.env.FLEET_PIPE_CONNECTION_FILE,profilesDirectory:process.env.FLEET_PROFILES_DIRECTORY||fileURLToPath(new URL('../../config/agents/',import.meta.url)),browserProxyAuth:process.env.FLEET_BROWSER_PROXY_AUTH==='true',browserOrigin:process.env.FLEET_BROWSER_ORIGIN,maxArtifactBytes:Number(process.env.FLEET_MAX_ARTIFACT_BYTES||16777216),database,store,browserSecret:process.env.FLEET_BROWSER_SECRET||'',machineSecret:process.env.FLEET_MACHINE_SECRET||'',hostId:process.env.FLEET_HOST_ID||'owner-mac'});
await app.listen({host:'127.0.0.1',port:Number(process.env.PORT||8787)});
