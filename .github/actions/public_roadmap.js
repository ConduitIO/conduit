const { graphql } = require("@octokit/graphql");

const main = async function() {
  console.log("PROJECT_ID: " + process.env.PROJECT_ID);

  const graphqlWithAuth = graphql.defaults({
    headers: {
      authorization: `token ` + process.env.GITHUB_TOKEN,
    },
  });

  var queryResult;
  var issueToFind = process.env.ISSUE_ID;
  var projectToFind = process.env.PROJECT_ID;
  var found_id;

  // Find the issue on the project boards. Don't think we'll have
  // tons of project boards, so we'll hard code this in.
  //
  console.log("LOOKING FOR ISSUE: " + issueToFind);
  queryResult = await graphqlWithAuth(`
    query findIssue($issue_id: ID!) {
      node(id: $issue_id) {
        ... on Issue {
          projectNextItems(first: 100) {
            totalCount
            nodes {
              id
              title
              project {
                id
              }
            }
          }
        }
      }
    }
  `,
  {
    issue_id: issueToFind
  });
  console.log("queryResult: " + JSON.stringify(queryResult));

  nodes = queryResult["node"]["projectNextItems"]["nodes"];
  for(let node_id = 0; node_id < nodes.length; node_id++) {
    console.log(JSON.stringify(nodes[node_id]));
    if (nodes[node_id]["project"]["id"] === projectToFind) {
      found_id = nodes[node_id]["id"];
    }
  }

  if (found_id.length > 1) {
    console.log("DELETING PROJECT ISSUE: " + issueToFind);
    await graphqlWithAuth(`
      mutation($project:ID!, $issue:ID!) {
        deleteProjectNextItem(input: {projectId: $project, itemId: $issue}) {
          deletedItemId
        }
      }
    `,
    {
      project: projectToFind,
      issue: found_id,
    })
  } else {
    console.log("COULDNT FIND ISSUE " + issueToFind + " on Project " + projectToFind);
  }

}
main();


