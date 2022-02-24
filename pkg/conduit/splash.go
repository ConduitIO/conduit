// Copyright © 2022 Meroxa, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package conduit

import "fmt"

func Splash() string {
	return splash1() + "\n" +
		splash2() + "\n" +
		splash3()
}

func splash1() string {
	const splash = `
        __________       
       /          \      
  ____/    ____    \____ 
 /        /    \        \
 \____    \____/    ____/
      \            /     
       \__________/      Conduit %s
`
	return fmt.Sprintf(splash, Version(true))
}

func splash2() string {
	const splash = "" +
		"             ....            \n" +
		"         .::::::::::.        \n" +
		"       .:::::‘‘‘‘:::::.      \n" +
		"      .::::        ::::.     \n" +
		" .::::::::          ::::::::.\n" +
		" `::::::::          ::::::::‘\n" +
		"      `::::        ::::‘     \n" +
		"       `:::::....:::::‘      \n" +
		"         `::::::::::‘        \n" +
		"             ‘‘‘‘            Conduit %s"
	return fmt.Sprintf(splash, Version(true))
}

func splash3() string {
	const splash = `
            &BGGPPGB#&           
         #PJ??????????YG&        
       &5????J5PGGPY?????G       
      #J???Y#       &G????P      
 GYJJJ????J&          G????JJJJ5&
 P????????Y           B????????J#
     &B????P&        #J???Y#     
       BJ????5GB##BPY????5&      
        &GJ????????????5B        
           #BP5YYY55GB&          Conduit %s
`
	return fmt.Sprintf(splash, Version(true))
}
