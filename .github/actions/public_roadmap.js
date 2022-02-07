const { graphql } = require("@octokit/graphql");

const main = async function() {
  console.log("PROJECT_ID: " + process.env.PROJECT_ID);

  const graphqlWithAuth = graphql.defaults({
    headers: {
      authorization: `token ` + process.env.GITHUB_TOKEN,
    },
  });

  var found_id = "";
  var title = process.env.ISSUE_TITLE;
  var queryResult;
  var cursor = "";

  console.log("LOOKING FOR ISSUE: " + title);

  do {
    if (cursor.length == 0) {
      queryResult = await graphqlWithAuth(`
      query {
        organization(login: "conduitio") {
          projectNext(number: 3) {
            items(first: 100) {
              totalCount
              pageInfo {
                endCursor
                hasNextPage
              }
              nodes {
                id
                title
                databaseId
              }
            }
          }
        }
      }
      `);
    } else {
      queryResult = await graphqlWithAuth(`
      query pageResults($afterid: String!) {
        organization(login: "conduitio") {
          projectNext(number: 3) {
            items(first: 100, after: $afterid) {
              totalCount
              pageInfo {
                endCursor
                hasNextPage
              }
              nodes {
                id
                title
                databaseId
              }
            }
          }
        }
      }
      `,
      {
        afterid: cursor
      });
    }

    console.log("queryResult: " + JSON.stringify(queryResult));
    last_cursor = queryResult["organization"]["projectNext"]["items"]["pageInfo"]["endCursor"];
    nodes = queryResult["organization"]["projectNext"]["items"]["nodes"];

    for(let node_id = 0; node_id < nodes.length; node_id++) {
      console.log(JSON.stringify(nodes[node_id]));
      if (nodes[node_id]["title"] === title) {
        found_id = nodes[node_id]["id"];
      }
    }
  } while (queryResult["organization"]["projectNext"]["items"]["pageInfo"]["hasNextPage"] && found_id.length == 0);


  // delete from project
  if (found_id.length > 1) {
    console.log("DELETING PROJECT ISSUE");
    console.log("  - ID: " + found_id);
    await graphqlWithAuth(`
      mutation($project:ID!, $issue:ID!) {
        deleteProjectNextItem(input: {projectId: $project, itemId: $issue}) {
          deletedItemId
        }
      }
    `,
    {
      project: process.env.PROJECT_ID,
      issue: found_id,
    })
  }


}
main();


