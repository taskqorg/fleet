import {fileURLToPath} from 'node:url';
import {createApp} from './app.ts';
import {createStore} from './store.ts';
// FLEET_DATABASE_URL: the coordinator role (fleet_app on PDS18, e.g. postgresql://fleet_app@/fleet?host=/var/run/postgresql).
// FLEET_MIGRATION_DATABASE_URL: the owner role (fleet) that applies db/schema.sql on start; defaults to FLEET_DATABASE_URL.
if(!process.env.FLEET_PEOPLE_FILE)throw Error('FLEET_PEOPLE_FILE required');
const database=process.env.FLEET_DATABASE_URL;if(!database)throw Error('FLEET_DATABASE_URL required');
const owner=createStore(process.env.FLEET_MIGRATION_DATABASE_URL||database);await owner.migrate();await owner.close();
const store=createStore(database);
const app=createApp({appDir:process.env.FLEET_APP_DIR,peopleFile:process.env.FLEET_PEOPLE_FILE,changeLogRetentionDays:Number(process.env.FLEET_CHANGE_LOG_RETENTION_DAYS||7),defaultAccountBinding:process.env.FLEET_DEFAULT_ACCOUNT_BINDING,testSiteRoot:process.env.FLEET_TEST_SITE_ROOT,projectConfigDirectory:process.env.FLEET_PROJECT_CONFIG_DIRECTORY||fileURLToPath(new URL('../../config/',import.meta.url)),connectionsDir:process.env.FLEET_CONNECTIONS_DIR,profilesDirectory:process.env.FLEET_PROFILES_DIRECTORY||fileURLToPath(new URL('../../config/agents/',import.meta.url)),labDirectory:process.env.FLEET_LAB_DIRECTORY||fileURLToPath(new URL('../../config/lab/',import.meta.url)),browserProxyAuth:process.env.FLEET_BROWSER_PROXY_AUTH==='true',browserOrigin:process.env.FLEET_BROWSER_ORIGIN,storeEndpointHosts:(process.env.FLEET_STORE_ENDPOINT_HOSTS||'').split(',').map(s=>s.trim()).filter(Boolean),storeSecrets:process.env.FLEET_STORE_SECRETS||fileURLToPath(new URL('../../var/stores/',import.meta.url)),cfAccessFile:process.env.FLEET_CF_ACCESS_FILE,defaultArtifactLocations:(process.env.FLEET_DEFAULT_ARTIFACT_LOCATIONS||'pds18').split(',').map(s=>s.trim()).filter(Boolean),artifactRetryWindowMs:Number(process.env.FLEET_ARTIFACT_RETRY_WINDOW_MS||86400000),database,store,browserSecret:process.env.FLEET_BROWSER_SECRET||'',hostId:process.env.FLEET_HOST_ID||'owner-mac'});
await app.listen({host:'127.0.0.1',port:Number(process.env.PORT||8787)});
