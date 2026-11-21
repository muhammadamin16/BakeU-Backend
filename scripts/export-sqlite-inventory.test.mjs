import test from "node:test";
import assert from "node:assert/strict";
import { DatabaseSync } from "node:sqlite";
import { mkdtemp, readFile, stat, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { exportSQLiteInventory } from "./export-sqlite-inventory.mjs";

test("inventory preserves the SQLite snapshot and omits hashes, salts and invented proof",async()=>{
  const dir=await mkdtemp(join(tmpdir(),"bakeu-inventory-")),source=join(dir,"backup.sqlite"),output=join(dir,"inventory.json");
  const db=new DatabaseSync(source);db.exec("CREATE TABLE users(id TEXT PRIMARY KEY,email TEXT,phone TEXT,role TEXT,profile TEXT,password_hash TEXT,salt TEXT,created_at TEXT);");
  const hash="ab".repeat(64),salt="cd".repeat(16);
  db.prepare("INSERT INTO users VALUES(?,?,?,?,?,?,?,?)").run("legacy-id","buyer@test.invalid","+998901234567","client",JSON.stringify({id:"forged-id",role:"admin",firstName:"Buyer",phoneVerified:true,secret:"never-export"}),hash,salt,"2026-01-01");db.close();
  const before=await readFile(source);
  try{
    assert.deepEqual(await exportSQLiteInventory(source,output),{users:1,applications:0,foreignKeyViolations:0});
    const text=await readFile(output,"utf8"),data=JSON.parse(text);
    assert.equal(data.users[0].sourceId,"legacy-id");assert.equal(data.users[0].role,"client");assert.equal(data.users[0].passwordAlgorithm,"node-scrypt");assert.equal(data.users[0].phoneOwnershipEvidence,"not-retained");
    assert.ok(!text.includes(hash));assert.ok(!text.includes(salt));assert.ok(!text.includes("never-export"));assert.deepEqual(await readFile(source),before);assert.equal((await stat(output)).mode&0o777,0o600);
    await assert.rejects(exportSQLiteInventory(source,output));assert.deepEqual(await readFile(source),before);
  }finally{await rm(dir,{recursive:true,force:true});}
});
