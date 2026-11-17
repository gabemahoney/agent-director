// Generation-specific shipped subprocess client. The qualification owns cases.
import {createInterface} from 'node:readline';
import {pathToFileURL} from 'node:url';

const [clientModule, binary, storePath] = process.argv.slice(2);
const {Client} = await import(pathToFileURL(clientModule).href);
const client = await Client.create({_cliPath: binary, storePath, home: process.env.HOME, callTimeoutMs: 10000});
console.log(JSON.stringify({ready: true, binary: client.binaryPath}));
try {
  for await (const line of createInterface({input: process.stdin})) {
    const {op, params} = JSON.parse(line);
    try {
      let result;
      switch (op) {
        case 'send': result = await client.sendKeys(params); break;
        case 'lookup': result = await client.getDelivery(params); break;
        case 'force-lookup': result = await client.getForce(params); break;
        default: throw Error(`unknown TS operation ${op}`);
      }
      console.log(JSON.stringify({result}));
    } catch (error: any) {
      console.log(JSON.stringify({result: null, error: String(error), err_name: error.err_name, err_details: error.err_details}));
    }
  }
} finally {
  await client.close();
}
